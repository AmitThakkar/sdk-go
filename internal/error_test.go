package internal

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nexus-rpc/sdk-go/nexus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commandpb "go.temporal.io/api/command/v1"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/converter"
	ilog "go.temporal.io/sdk/internal/log"
)

const (
	// assume this is some error reason defined by activity implementation.
	applicationErrReasonA = "CustomReasonA"
)

type testStruct struct {
	Name string
	Age  int
}

type testStruct2 struct {
	Name      string
	Age       int
	Favorites *[]string
}

var (
	testErrorDetails1 = "my details"
	testErrorDetails2 = 123
	testErrorDetails3 = testStruct{"a string", 321}
	testErrorDetails4 = testStruct2{"a string", 321, &[]string{"eat", "code"}}
)

func Test_GenericGoError(t *testing.T) {
	// test activity error
	errorActivityFn := func() error {
		return errors.New("activity error")
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err := env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)

	var activityErr *ActivityError
	require.True(t, errors.As(err, &activityErr))

	err = errors.Unwrap(activityErr)
	var applicationErr *ApplicationError
	require.True(t, errors.As(err, &applicationErr), err)

	require.Equal(t, "activity error", err.Error())

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return errors.New("workflow error")
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)

	var workflowErr *WorkflowExecutionError
	require.True(t, errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	require.True(t, errors.As(err, &applicationErr))
	require.Equal(t, "workflow error", err.Error())
}

func Test_ActivityErrorAccessors(t *testing.T) {
	require := require.New(t)
	err := NewApplicationError("app err", "", true, nil)
	var applicationErr *ApplicationError
	require.True(errors.As(err, &applicationErr))
	err = NewActivityError(8, 22, "alex", &commonpb.ActivityType{Name: "activityType"}, "32283", enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE, applicationErr)
	var activityErr *ActivityError
	require.True(errors.As(err, &activityErr))
	require.Equal("32283", activityErr.ActivityID())
	require.Equal(&commonpb.ActivityType{Name: "activityType"}, activityErr.ActivityType())
	require.Equal(enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE, activityErr.RetryState())
	require.Equal("alex", activityErr.Identity())
	require.Equal(int64(8), activityErr.ScheduledEventID())
	require.Equal(int64(22), activityErr.StartedEventID())
}

func Test_ActivityNotRegistered(t *testing.T) {
	registeredActivityFn, unregisteredActivitFn := "RegisteredActivity", "UnregisteredActivityFn"
	s := &WorkflowTestSuite{}
	s.SetLogger(ilog.NewNopLogger())
	env := s.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(func() error { return nil }, RegisterActivityOptions{Name: registeredActivityFn})
	_, err := env.ExecuteActivity(unregisteredActivitFn)
	require.Error(t, err)
	require.Contains(t, err.Error(), fmt.Sprintf("unable to find activityType=%v", unregisteredActivitFn))
	require.Contains(t, err.Error(), registeredActivityFn)
}

func Test_TimeoutError(t *testing.T) {
	err := NewTimeoutError("timeout", enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START, nil)
	var timeoutErr *TimeoutError
	require.True(t, errors.As(err, &timeoutErr))

	require.False(t, timeoutErr.HasLastHeartbeatDetails())
	var data string
	require.Equal(t, ErrNoData, timeoutErr.LastHeartbeatDetails(&data))

	err = NewHeartbeatTimeoutError(testErrorDetails1)
	var heartbeatErr *TimeoutError
	require.True(t, errors.As(err, &heartbeatErr))
	require.True(t, heartbeatErr.HasLastHeartbeatDetails())
	require.NoError(t, heartbeatErr.LastHeartbeatDetails(&data))
	require.Equal(t, testErrorDetails1, data)
}

func Test_TimeoutError_WithDetails(t *testing.T) {
	testTimeoutErrorDetails(t, enumspb.TIMEOUT_TYPE_HEARTBEAT)
	testTimeoutErrorDetails(t, enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE)
	testTimeoutErrorDetails(t, enumspb.TIMEOUT_TYPE_START_TO_CLOSE)
}

func testTimeoutErrorDetails(t *testing.T, timeoutType enumspb.TimeoutType) {
	context := &workflowEnvironmentImpl{
		commandsHelper:   newCommandsHelper(),
		dataConverter:    converter.GetDefaultDataConverter(),
		failureConverter: GetDefaultFailureConverter(),
	}
	h := newCommandsHelper()
	var actualErr error
	activityID := "activityID"
	context.commandsHelper.scheduledEventIDToActivityID[5] = activityID
	di := h.newActivityCommandStateMachine(
		5,
		&commandpb.ScheduleActivityTaskCommandAttributes{ActivityId: activityID}, nil)
	di.state = commandStateInitiated
	di.setData(&scheduledActivity{
		callback: func(r *commonpb.Payloads, e error) {
			actualErr = e
		},
	})
	context.commandsHelper.addCommand(di)
	encodedDetails1, _ := context.dataConverter.ToPayloads(testErrorDetails1)
	event := createTestEventActivityTaskTimedOut(7, &historypb.ActivityTaskTimedOutEventAttributes{
		Failure: &failurepb.Failure{
			FailureInfo: &failurepb.Failure_TimeoutFailureInfo{TimeoutFailureInfo: &failurepb.TimeoutFailureInfo{
				LastHeartbeatDetails: encodedDetails1,
				TimeoutType:          timeoutType,
			}},
		},
		RetryState:       enumspb.RETRY_STATE_TIMEOUT,
		ScheduledEventId: 5,
		StartedEventId:   6,
	})
	weh := &workflowExecutionEventHandlerImpl{context, nil}
	_ = weh.handleActivityTaskTimedOut(event)
	var timeoutErr *TimeoutError
	ok := errors.As(actualErr, &timeoutErr)
	require.True(t, ok)
	require.True(t, timeoutErr.HasLastHeartbeatDetails())
	var data string
	require.NoError(t, timeoutErr.LastHeartbeatDetails(&data))
	require.Equal(t, testErrorDetails1, data)
}

func Test_ApplicationError(t *testing.T) {
	// test ErrorDetailValues as Details
	var a1 string
	var a2 int
	var a3 testStruct
	err0 := NewApplicationError(applicationErrReasonA, "", false, nil, testErrorDetails1)
	var applicationErr0 *ApplicationError
	require.True(t, errors.As(err0, &applicationErr0))
	require.True(t, applicationErr0.HasDetails())
	_ = applicationErr0.Details(&a1)
	require.Equal(t, testErrorDetails1, a1)
	a1 = ""
	err0 = NewApplicationError(applicationErrReasonA, "", false, nil, testErrorDetails1, testErrorDetails2, testErrorDetails3)
	require.True(t, errors.As(err0, &applicationErr0))
	require.True(t, applicationErr0.HasDetails())
	_ = applicationErr0.Details(&a1, &a2, &a3)
	require.Equal(t, testErrorDetails1, a1)
	require.Equal(t, testErrorDetails2, a2)
	require.Equal(t, testErrorDetails3, a3)

	// test EncodedValues as Details
	errorActivityFn := func() error {
		return err0
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err := env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)
	var activityErr *ActivityError
	require.True(t, errors.As(err, &activityErr))

	err = errors.Unwrap(activityErr)
	var err1 *ApplicationError
	require.True(t, errors.As(err, &err1), err)
	require.True(t, err1.HasDetails())
	var b1 string
	var b2 int
	var b3 testStruct
	_ = err1.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)

	// test reason and no detail
	newReason := "another reason"
	err2 := NewApplicationError(newReason, "", false, nil)
	var applicationErr2 *ApplicationError
	require.True(t, errors.As(err2, &applicationErr2))
	require.True(t, !applicationErr2.HasDetails())
	require.Equal(t, ErrNoData, applicationErr2.Details())
	require.Equal(t, newReason, applicationErr2.Error())
	err3 := NewApplicationError(newReason, "", false, nil, nil)
	var applicationErr3 *ApplicationError
	require.True(t, errors.As(err3, &applicationErr3))
	// TODO: probably we want to handle this case when details are nil, HasDetails return false
	require.True(t, applicationErr3.HasDetails())

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return err0
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	var workflowErr *WorkflowExecutionError
	require.True(t, errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	var err4 *ApplicationError
	require.True(t, errors.As(err, &err4))
	require.True(t, err4.HasDetails())
	_ = err4.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)
}

func Test_ApplicationError_Pointer(t *testing.T) {
	a1 := testStruct2{}
	err1 := NewApplicationError(applicationErrReasonA, "", false, nil, testErrorDetails4)
	var applicationErr1 *ApplicationError
	require.True(t, errors.As(err1, &applicationErr1))
	require.True(t, applicationErr1.HasDetails())
	err := applicationErr1.Details(&a1)
	require.NoError(t, err)
	require.Equal(t, testErrorDetails4, a1)

	a2 := &testStruct2{}
	err2 := NewApplicationError(applicationErrReasonA, "", false, nil, &testErrorDetails4) // // pointer in details
	var applicationErr2 *ApplicationError
	require.True(t, errors.As(err2, &applicationErr2))
	require.True(t, applicationErr2.HasDetails())
	err = applicationErr2.Details(&a2)
	require.NoError(t, err)
	require.Equal(t, &testErrorDetails4, a2)

	// test EncodedValues as Details
	errorActivityFn := func() error {
		return err1
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err = env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)

	var activityErr *ActivityError
	require.True(t, errors.As(err, &activityErr))

	err = errors.Unwrap(activityErr)
	var err3 *ApplicationError
	require.True(t, errors.As(err, &err3), err)
	require.True(t, err3.HasDetails())
	b1 := testStruct2{}
	require.NoError(t, err3.Details(&b1))
	require.Equal(t, testErrorDetails4, b1)

	errorActivityFn2 := func() error {
		return err2 // pointer in details
	}
	env.RegisterActivity(errorActivityFn2)
	_, err = env.ExecuteActivity(errorActivityFn2)
	require.Error(t, err)
	require.True(t, errors.As(err, &activityErr))

	err = errors.Unwrap(activityErr)
	var err4 *ApplicationError
	require.True(t, errors.As(err, &err4))
	require.True(t, err4.HasDetails())
	b2 := &testStruct2{}
	require.NoError(t, err4.Details(&b2))
	require.Equal(t, &testErrorDetails4, b2)

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return err1
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	var workflowErr *WorkflowExecutionError
	require.True(t, errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	var err5 *ApplicationError
	require.True(t, errors.As(err, &err5))
	require.True(t, err5.HasDetails())
	_ = err5.Details(&b1)
	require.NoError(t, err5.Details(&b1))
	require.Equal(t, testErrorDetails4, b1)

	errorWorkflowFn2 := func(ctx Context) error {
		return err2 // pointer in details
	}
	wfEnv = s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn2)
	wfEnv.ExecuteWorkflow(errorWorkflowFn2)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	require.True(t, errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	var err6 *ApplicationError
	require.True(t, errors.As(err, &err6))
	require.True(t, err6.HasDetails())
	_ = err6.Details(&b2)
	require.NoError(t, err6.Details(&b2))
	require.Equal(t, &testErrorDetails4, b2)
}

func Test_CanceledError(t *testing.T) {
	// test ErrorDetailValues as Details
	var a1 string
	var a2 int
	var a3 testStruct
	err0 := NewCanceledError(testErrorDetails1)
	var canceledErr0 *CanceledError
	require.True(t, errors.As(err0, &canceledErr0))
	require.True(t, canceledErr0.HasDetails())
	_ = canceledErr0.Details(&a1)
	require.Equal(t, testErrorDetails1, a1)
	a1 = ""
	err0 = NewCanceledError(testErrorDetails1, testErrorDetails2, testErrorDetails3)
	require.True(t, errors.As(err0, &canceledErr0))
	require.True(t, canceledErr0.HasDetails())
	_ = canceledErr0.Details(&a1, &a2, &a3)
	require.Equal(t, testErrorDetails1, a1)
	require.Equal(t, testErrorDetails2, a2)
	require.Equal(t, testErrorDetails3, a3)

	// test EncodedValues as Details
	errorActivityFn := func() error {
		return err0
	}
	s := &WorkflowTestSuite{}
	env := s.NewTestActivityEnvironment()
	env.RegisterActivity(errorActivityFn)
	_, err := env.ExecuteActivity(errorActivityFn)
	require.Error(t, err)
	var activityErr *ActivityError
	require.True(t, errors.As(err, &activityErr))

	err = errors.Unwrap(activityErr)
	var err1 *CanceledError
	require.True(t, errors.As(err, &err1), err)
	require.True(t, err1.HasDetails())
	var b1 string
	var b2 int
	var b3 testStruct
	_ = err1.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)

	err2 := NewCanceledError()
	var canceledErr2 *CanceledError
	require.True(t, errors.As(err2, &canceledErr2))
	require.False(t, canceledErr2.HasDetails())

	// test workflow error
	errorWorkflowFn := func(ctx Context) error {
		return err0
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflow(errorWorkflowFn)
	wfEnv.ExecuteWorkflow(errorWorkflowFn)
	err = wfEnv.GetWorkflowError()
	require.Error(t, err)
	var workflowErr *WorkflowExecutionError
	require.True(t, errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	var err3 *CanceledError
	require.True(t, errors.As(err, &err3))
	require.True(t, err3.HasDetails())
	_ = err3.Details(&b1, &b2, &b3)
	require.Equal(t, testErrorDetails1, b1)
	require.Equal(t, testErrorDetails2, b2)
	require.Equal(t, testErrorDetails3, b3)
}

func Test_IsCanceledError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "empty detail",
			err:      NewCanceledError(),
			expected: true,
		},
		{
			name:     "with detail",
			err:      NewCanceledError("details"),
			expected: true,
		},
		{
			name:     "not canceled error",
			err:      errors.New("details"),
			expected: false,
		},
	}

	for _, test := range tests {
		require.Equal(t, test.expected, IsCanceledError(test.err))
	}
}

func TestErrorDetailsValues(t *testing.T) {
	e := ErrorDetailsValues{}
	require.Equal(t, ErrNoData, e.Get())

	e = ErrorDetailsValues{testErrorDetails1, testErrorDetails2, testErrorDetails3}
	var a1 string
	var a2 int
	var a3 testStruct
	require.True(t, e.HasValues())
	_ = e.Get(&a1)
	require.Equal(t, testErrorDetails1, a1)
	_ = e.Get(&a1, &a2, &a3)
	require.Equal(t, testErrorDetails1, a1)
	require.Equal(t, testErrorDetails2, a2)
	require.Equal(t, testErrorDetails3, a3)

	require.Equal(t, ErrTooManyArg, e.Get(&a1, &a2, &a3, &a3))
}

func Test_SignalExternalWorkflowExecutionFailedError(t *testing.T) {
	context := &workflowEnvironmentImpl{
		commandsHelper: newCommandsHelper(),
		dataConverter:  converter.GetDefaultDataConverter(),
	}
	h := newCommandsHelper()
	var actualErr error
	var initiatedEventID int64 = 101
	signalID := "signalID"
	context.commandsHelper.scheduledEventIDToSignalID[initiatedEventID] = signalID
	di := h.newSignalExternalWorkflowStateMachine(
		&commandpb.SignalExternalWorkflowExecutionCommandAttributes{},
		signalID,
	)
	di.state = commandStateInitiated
	di.setData(&scheduledSignal{
		callback: func(r *commonpb.Payloads, e error) {
			actualErr = e
		},
	})
	context.commandsHelper.addCommand(di)
	weh := &workflowExecutionEventHandlerImpl{context, nil}
	event := createTestEventSignalExternalWorkflowExecutionFailed(1, &historypb.SignalExternalWorkflowExecutionFailedEventAttributes{
		InitiatedEventId: initiatedEventID,
		Cause:            enumspb.SIGNAL_EXTERNAL_WORKFLOW_EXECUTION_FAILED_CAUSE_EXTERNAL_WORKFLOW_EXECUTION_NOT_FOUND,
	})
	require.NoError(t, weh.handleSignalExternalWorkflowExecutionFailed(event))
	_, ok := actualErr.(*UnknownExternalWorkflowExecutionError)
	require.True(t, ok)
}

func Test_ContinueAsNewError(t *testing.T) {
	a1 := 1234
	a2 := "some random input"

	continueAsNewWfName := "continueAsNewWorkflowFn"
	continueAsNewWorkflowFn := func(ctx Context, testInt int, testString string) error {
		return NewContinueAsNewError(ctx, continueAsNewWfName, a1, a2)
	}

	dataConverter := converter.GetDefaultDataConverter()
	headerValue, err := dataConverter.ToPayload("test-data")
	assert.NoError(t, err)
	header := &commonpb.Header{
		Fields: map[string]*commonpb.Payload{"test": headerValue},
	}

	s := &WorkflowTestSuite{
		header:             header,
		contextPropagators: []ContextPropagator{NewKeysPropagator([]string{"test"})},
	}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflowWithOptions(continueAsNewWorkflowFn, RegisterWorkflowOptions{
		Name: continueAsNewWfName,
	})
	wfEnv.ExecuteWorkflow(continueAsNewWorkflowFn, 101, "another random string")
	err = wfEnv.GetWorkflowError()

	require.Error(t, err)
	var workflowErr *WorkflowExecutionError
	require.True(t, errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	var continueAsNewErr *ContinueAsNewError
	require.True(t, errors.As(err, &continueAsNewErr))
	require.Equal(t, continueAsNewWfName, continueAsNewErr.WorkflowType.Name)

	input := continueAsNewErr.Input
	var intArg int
	var stringArg string
	err = dataConverter.FromPayloads(input, &intArg, &stringArg)
	require.NoError(t, err)
	require.Equal(t, a1, intArg)
	require.Equal(t, a2, stringArg)
	require.Equal(t, header, continueAsNewErr.Header)

	require.Nil(t, continueAsNewErr.RetryPolicy)
}

func Test_ContinueAsNewErrorWithOptions(t *testing.T) {
	const (
		a1                        = 1234
		a2                        = "some random input"
		continueAsNewWfName       = "continueAsNewWorkflowFn"
		initialInterval           = 2 * time.Second
		backoffCoefficient        = 1.1
		maximumAttempts     int32 = 23
		maximumInterval           = time.Minute
	)

	require := require.New(t)
	continueAsNewWorkflowFn := func(ctx Context, testInt int, testString string) error {
		err := NewContinueAsNewErrorWithOptions(
			ctx,
			ContinueAsNewErrorOptions{RetryPolicy: &RetryPolicy{
				BackoffCoefficient: backoffCoefficient,
				InitialInterval:    initialInterval,
				MaximumAttempts:    maximumAttempts,
				MaximumInterval:    maximumInterval,
			}},
			continueAsNewWfName,
			a1,
			a2,
		)

		continueAsNewErr := err.(*ContinueAsNewError)
		if continueAsNewErr.RetryPolicy == nil {
			return errors.New("retry policy is nil")
		}

		if continueAsNewErr.RetryPolicy.MaximumAttempts != maximumAttempts {
			return errors.New("retry policy maximum attempts is not set")
		}

		return err
	}

	s := &WorkflowTestSuite{}
	wfEnv := s.NewTestWorkflowEnvironment()
	wfEnv.RegisterWorkflowWithOptions(continueAsNewWorkflowFn, RegisterWorkflowOptions{
		Name: continueAsNewWfName,
	})
	wfEnv.ExecuteWorkflow(continueAsNewWorkflowFn, 101, "another random string")

	err := wfEnv.GetWorkflowError()

	require.Error(err)
	var workflowErr *WorkflowExecutionError
	require.True(errors.As(err, &workflowErr))

	err = errors.Unwrap(workflowErr)
	var continueAsNewErr *ContinueAsNewError
	require.True(errors.As(err, &continueAsNewErr))
	require.Equal(continueAsNewWfName, continueAsNewErr.WorkflowType.Name)

	input := continueAsNewErr.Input
	var intArg int
	var stringArg string
	dataConverter := converter.GetDefaultDataConverter()
	err = dataConverter.FromPayloads(input, &intArg, &stringArg)
	require.NoError(err)
	require.Equal(a1, intArg)
	require.Equal(a2, stringArg)

	require.NotNil(continueAsNewErr.RetryPolicy)
	require.Equal(backoffCoefficient, continueAsNewErr.RetryPolicy.BackoffCoefficient)
	require.Equal(initialInterval, continueAsNewErr.RetryPolicy.InitialInterval)
	require.Equal(maximumAttempts, continueAsNewErr.RetryPolicy.MaximumAttempts)
	require.Equal(maximumInterval, continueAsNewErr.RetryPolicy.MaximumInterval)
}

type coolError struct{}

func (e coolError) Error() string {
	return "cool error"
}

func Test_GetErrorType(t *testing.T) {
	require := require.New(t)
	err := errors.New("some error")
	errType := getErrType(err)
	require.Equal("", errType)

	err = coolError{}
	errType = getErrType(err)
	require.Equal("coolError", errType)

	err2 := &coolError{}
	errType2 := getErrType(err2)
	require.Equal("coolError", errType2)
}

func Test_IsRetryable(t *testing.T) {
	require := require.New(t)
	require.False(IsRetryable(newTerminatedError(), nil))
	require.False(IsRetryable(NewCanceledError(), nil))
	require.False(IsRetryable(newWorkflowPanicError("", ""), nil))

	require.True(IsRetryable(NewTimeoutError("timeout", enumspb.TIMEOUT_TYPE_START_TO_CLOSE, nil), nil))
	require.False(IsRetryable(NewTimeoutError("timeout", enumspb.TIMEOUT_TYPE_SCHEDULE_TO_START, nil), nil))
	require.False(IsRetryable(NewTimeoutError("timeout", enumspb.TIMEOUT_TYPE_SCHEDULE_TO_CLOSE, nil), nil))
	require.True(IsRetryable(NewTimeoutError("timeout", enumspb.TIMEOUT_TYPE_HEARTBEAT, nil), nil))

	require.False(IsRetryable(NewApplicationError("", "", true, nil), nil))
	require.True(IsRetryable(NewApplicationError("", "", false, nil), nil))

	applicationErr := NewApplicationError("", "MyCoolErr", false, nil)
	require.True(IsRetryable(applicationErr, nil))
	require.False(IsRetryable(applicationErr, []string{"MyCoolErr"}))

	coolErr := &coolError{}
	require.True(IsRetryable(coolErr, nil))
	require.False(IsRetryable(coolErr, []string{"coolError"}))
	require.True(IsRetryable(coolErr, []string{"anotherError"}))
	require.False(IsRetryable(coolErr, []string{"anotherError", "coolError"}))
}

func Test_convertErrorToFailure_ApplicationError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := NewApplicationError("message", "customType", true, errors.New("cause error"), "details", 2208)
	f := fc.ErrorToFailure(err)
	require.Equal("message", f.GetMessage())
	require.Equal("customType", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Equal([]byte(`"details"`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[0].GetData())
	require.Equal([]byte(`2208`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[1].GetData())
	require.Equal("cause error", f.GetCause().GetMessage())
	require.Equal("", f.GetCause().GetApplicationFailureInfo().GetType())
	require.Nil(f.GetCause().GetCause())

	err2 := fc.FailureToError(f)
	var applicationErr *ApplicationError
	require.True(errors.As(err2, &applicationErr))
	require.Equal("message (type: customType, retryable: false): cause error", applicationErr.Error())

	err2 = errors.Unwrap(err2)
	require.True(errors.As(err2, &applicationErr))
	require.Equal("cause error", applicationErr.Error())
}

func Test_convertErrorToFailure_ApplicationErrorWithExtraRequests(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := NewApplicationErrorWithOptions(
		"message",
		"customType",
		ApplicationErrorOptions{
			NonRetryable: true,
			Cause:        errors.New("cause error"),
			Details:      []interface{}{"details", 2208},
			Category:     ApplicationErrorCategoryBenign,
		},
	)
	f := fc.ErrorToFailure(err)
	require.Equal("message", f.GetMessage())
	require.Equal("customType", f.GetApplicationFailureInfo().GetType())
	require.True(f.GetApplicationFailureInfo().GetNonRetryable())
	require.Equal([]byte(`"details"`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[0].GetData())
	require.Equal([]byte(`2208`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[1].GetData())
	require.Equal("cause error", f.GetCause().GetMessage())
	require.Equal("", f.GetCause().GetApplicationFailureInfo().GetType())
	require.Nil(f.GetCause().GetCause())
	require.Equal(enumspb.APPLICATION_ERROR_CATEGORY_BENIGN, f.GetApplicationFailureInfo().GetCategory())

	err2 := fc.FailureToError(f)
	var applicationErr *ApplicationError
	require.True(errors.As(err2, &applicationErr))
	require.Equal("message (type: customType, retryable: false): cause error", applicationErr.Error())
	require.Equal(ApplicationErrorCategoryBenign, applicationErr.Category())

	err2 = errors.Unwrap(err2)
	require.True(errors.As(err2, &applicationErr))
	require.Equal("cause error", applicationErr.Error())

	err = NewApplicationErrorWithOptions(
		"another message",
		"another customType",
		ApplicationErrorOptions{
			Category: ApplicationErrorCategoryUnspecified,
		},
	)
	f = fc.ErrorToFailure(err)
	require.Equal(enumspb.APPLICATION_ERROR_CATEGORY_UNSPECIFIED, f.GetApplicationFailureInfo().GetCategory())
}

func Test_convertErrorToFailure_EncodeMessage(t *testing.T) {
	require := require.New(t)

	fc := NewDefaultFailureConverter(DefaultFailureConverterOptions{
		DataConverter:          converter.GetDefaultDataConverter(),
		EncodeCommonAttributes: true,
	})

	err := NewApplicationError("message", "customType", true, errors.New("cause error"), "details", 2208)
	f := fc.ErrorToFailure(err)
	require.Equal("Encoded failure", f.GetMessage())
	require.Equal("", f.GetStackTrace())
	require.Equal("customType", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Equal([]byte(`"details"`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[0].GetData())
	require.Equal([]byte(`2208`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[1].GetData())
	require.Equal("Encoded failure", f.GetCause().GetMessage())
	require.Equal("", f.GetCause().GetApplicationFailureInfo().GetType())
	require.Nil(f.GetCause().GetCause())

	err2 := fc.FailureToError(f)
	var applicationErr *ApplicationError
	require.True(errors.As(err2, &applicationErr))
	require.Equal("message (type: customType, retryable: false): cause error", applicationErr.Error())

	err2 = errors.Unwrap(err2)
	require.True(errors.As(err2, &applicationErr))
	require.Equal("cause error", applicationErr.Error())
}

func Test_EncodeDecodeEncodeMessage(t *testing.T) {
	require := require.New(t)

	fc := NewDefaultFailureConverter(DefaultFailureConverterOptions{
		DataConverter:          converter.GetDefaultDataConverter(),
		EncodeCommonAttributes: true,
	})

	subAppErr := NewApplicationError("sub message", "sub customType", true, errors.New("cause error"), "sub details", 2208)
	err := NewApplicationError("message", "customType", true, subAppErr, "details", 2208)
	f := fc.ErrorToFailure(err)
	// Check main error is encoded
	require.Equal("Encoded failure", f.GetMessage())
	require.Equal("", f.GetStackTrace())
	require.Equal("customType", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Equal([]byte(`"details"`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[0].GetData())
	require.Equal([]byte(`2208`), f.GetApplicationFailureInfo().GetDetails().GetPayloads()[1].GetData())
	// Check sub error in encoded
	require.Equal("Encoded failure", f.GetCause().GetMessage())
	require.Equal("", f.GetCause().GetStackTrace())
	require.Equal("sub customType", f.GetCause().GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetCause().GetApplicationFailureInfo().GetNonRetryable())
	// Check the sub sub error is encoded
	require.Equal("Encoded failure", f.GetCause().GetCause().GetMessage())
	require.Equal("", f.GetCause().GetCause().GetApplicationFailureInfo().GetType())
	require.Nil(f.GetCause().GetCause().GetCause())

	err2 := fc.FailureToError(f)
	var applicationErr *ApplicationError
	require.True(errors.As(err2, &applicationErr))
	require.Equal("message (type: customType, retryable: false): sub message (type: sub customType, retryable: false): cause error", applicationErr.Error())

	subErr := errors.Unwrap(err2)
	require.True(errors.As(subErr, &applicationErr))
	require.Equal("sub message (type: sub customType, retryable: false): cause error", applicationErr.Error())

	subSubErr := errors.Unwrap(subErr)
	require.True(errors.As(subSubErr, &applicationErr))
	require.Equal("cause error", applicationErr.Error())
	// Check main error is re-encoded properly
	f = fc.ErrorToFailure(err2)
	require.Equal("Encoded failure", f.GetMessage())
	require.Equal("", f.GetStackTrace())
	require.Equal("customType", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetCause().GetApplicationFailureInfo().GetNonRetryable())
	// Check sub error in re-encoded
	require.Equal("Encoded failure", f.GetCause().GetMessage())
	require.Equal("", f.GetCause().GetStackTrace())
	require.Equal("sub customType", f.GetCause().GetApplicationFailureInfo().GetType())
	// Check the sub sub error is encoded
	require.Equal("Encoded failure", f.GetCause().GetCause().GetMessage())
	require.Equal("", f.GetCause().GetCause().GetApplicationFailureInfo().GetType())
	require.Nil(f.GetCause().GetCause().GetCause())
}

func Test_convertErrorToFailure_EncodeStackTrace(t *testing.T) {
	require := require.New(t)
	fc := NewDefaultFailureConverter(DefaultFailureConverterOptions{
		DataConverter:          converter.GetDefaultDataConverter(),
		EncodeCommonAttributes: true,
	})

	err := newPanicError("panic message", "long call stack")
	var panicErr *PanicError
	require.True(errors.As(err, &panicErr))
	f := fc.ErrorToFailure(err)
	require.Equal("Encoded failure", f.GetMessage())
	require.Equal("", f.GetStackTrace())
	require.Equal("PanicError", f.GetApplicationFailureInfo().GetType())
	require.Equal(false, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Nil(f.GetCause())

	err2 := fc.FailureToError(f)
	var panicErr2 *PanicError
	require.True(errors.As(err2, &panicErr2))
	require.Equal(panicErr.Error(), panicErr2.Error())
	require.Equal(panicErr.StackTrace(), panicErr2.StackTrace())

	f = fc.ErrorToFailure(newWorkflowPanicError("panic message", "long call stack"))
	require.Equal("Encoded failure", f.GetMessage())
	require.Equal("", f.GetStackTrace())
	require.Equal("PanicError", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Nil(f.GetCause())

	err2 = fc.FailureToError(f)
	require.True(errors.As(err2, &panicErr2))
	require.Equal(panicErr.Error(), panicErr2.Error())
	require.Equal(panicErr.StackTrace(), panicErr2.StackTrace())
}

func Test_convertErrorToFailure_CanceledError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := NewCanceledError("details", 2208)
	f := fc.ErrorToFailure(err)
	require.Equal("canceled", f.GetMessage())
	require.Equal([]byte(`"details"`), f.GetCanceledFailureInfo().GetDetails().GetPayloads()[0].GetData())
	require.Equal([]byte(`2208`), f.GetCanceledFailureInfo().GetDetails().GetPayloads()[1].GetData())
	require.Nil(f.GetCause())

	err2 := fc.FailureToError(f)
	var canceledErr *CanceledError
	require.True(errors.As(err2, &canceledErr))
}

func Test_convertErrorToFailure_PanicError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := newPanicError("panic message", "long call stack")
	var panicErr *PanicError
	require.True(errors.As(err, &panicErr))
	f := fc.ErrorToFailure(err)
	require.Equal("panic message", f.GetMessage())
	require.Equal("PanicError", f.GetApplicationFailureInfo().GetType())
	require.Equal(false, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Equal("long call stack", f.GetStackTrace())
	require.Nil(f.GetCause())

	err2 := fc.FailureToError(f)
	var panicErr2 *PanicError
	require.True(errors.As(err2, &panicErr2))
	require.Equal(panicErr.Error(), panicErr2.Error())
	require.Equal(panicErr.StackTrace(), panicErr2.StackTrace())

	f = fc.ErrorToFailure(newWorkflowPanicError("panic message", "long call stack"))
	require.Equal("panic message", f.GetMessage())
	require.Equal("PanicError", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Equal("long call stack", f.GetStackTrace())
	require.Nil(f.GetCause())

	err2 = fc.FailureToError(f)
	require.True(errors.As(err2, &panicErr2))
	require.Equal(panicErr.Error(), panicErr2.Error())
	require.Equal(panicErr.StackTrace(), panicErr2.StackTrace())
}

func Test_convertErrorToFailure_TimeoutError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := NewTimeoutError("timeout", enumspb.TIMEOUT_TYPE_HEARTBEAT, &coolError{})
	var timeoutErr *TimeoutError
	require.True(errors.As(err, &timeoutErr))
	f := fc.ErrorToFailure(err)
	require.Equal("timeout", f.GetMessage())
	require.Equal(enumspb.TIMEOUT_TYPE_HEARTBEAT, f.GetTimeoutFailureInfo().GetTimeoutType())
	require.Equal(fc.ErrorToFailure(&coolError{}), f.GetCause())
	require.Equal(f.GetCause(), fc.ErrorToFailure(&coolError{}))

	err2 := fc.FailureToError(f)
	var timeoutErr2 *TimeoutError
	require.True(errors.As(err2, &timeoutErr2))
	require.Equal("timeout (type: Heartbeat): cool error (type: coolError, retryable: true)", timeoutErr2.Error())
	require.Equal(timeoutErr.TimeoutType(), timeoutErr2.TimeoutType())
}

func Test_convertErrorToFailure_TerminateError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := newTerminatedError()
	f := fc.ErrorToFailure(err)
	require.Equal("terminated", f.GetMessage())
	require.Nil(f.GetCause())

	err2 := fc.FailureToError(f)
	var terminateErr *TerminatedError
	require.True(errors.As(err2, &terminateErr))
}

func Test_convertErrorToFailure_ServerError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := NewServerError("message", true, &coolError{})
	var serverErr *ServerError
	require.True(errors.As(err, &serverErr))
	f := fc.ErrorToFailure(err)
	require.Equal("message", f.GetMessage())
	require.Equal(true, f.GetServerFailureInfo().GetNonRetryable())
	require.Equal(fc.ErrorToFailure(&coolError{}), f.GetCause())

	err2 := fc.FailureToError(f)
	var serverErr2 *ServerError
	require.True(errors.As(err2, &serverErr2))
	require.Equal("message: cool error (type: coolError, retryable: true)", serverErr2.Error())
	require.Equal(serverErr.nonRetryable, serverErr2.nonRetryable)
}

func Test_convertErrorToFailure_ActivityError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := NewApplicationError("app err", "", true, nil)
	var applicationErr *ApplicationError
	require.True(errors.As(err, &applicationErr))
	err = NewActivityError(8, 22, "alex", &commonpb.ActivityType{Name: "activityType"}, "32283", enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE, applicationErr)
	var activityErr *ActivityError
	require.True(errors.As(err, &activityErr))
	f := fc.ErrorToFailure(err)
	require.Equal("activity error", f.GetMessage())
	require.Equal(int64(8), f.GetActivityFailureInfo().GetScheduledEventId())
	require.Equal(int64(22), f.GetActivityFailureInfo().GetStartedEventId())
	require.Equal("alex", f.GetActivityFailureInfo().GetIdentity())
	require.Equal("activityType", f.GetActivityFailureInfo().GetActivityType().GetName())
	require.Equal("32283", f.GetActivityFailureInfo().GetActivityId())
	require.Equal(enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE, f.GetActivityFailureInfo().GetRetryState())
	require.Equal(fc.ErrorToFailure(applicationErr), f.GetCause())

	err2 := fc.FailureToError(f)
	var activityTaskErr *ActivityError
	require.True(errors.As(err2, &activityTaskErr))
	require.Equal(activityErr.Error(), activityTaskErr.Error())
	require.Equal(activityErr.startedEventID, activityTaskErr.startedEventID)

	var applicationErr2 *ApplicationError
	require.True(errors.As(err2, &applicationErr2))
	require.Equal(applicationErr.Error(), applicationErr2.Error())
	require.Equal(applicationErr.NonRetryable(), applicationErr2.NonRetryable())
}

func Test_convertErrorToFailure_ChildWorkflowExecutionError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	applicationErr := NewApplicationError("app err", "", true, nil)
	err := NewChildWorkflowExecutionError("namespace", "wID", "rID", "wfType", 8, 22, enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE, applicationErr)
	f := fc.ErrorToFailure(err)
	require.Equal("child workflow execution error", f.GetMessage())
	require.Equal(int64(8), f.GetChildWorkflowExecutionFailureInfo().GetInitiatedEventId())
	require.Equal(int64(22), f.GetChildWorkflowExecutionFailureInfo().GetStartedEventId())
	require.Equal("namespace", f.GetChildWorkflowExecutionFailureInfo().GetNamespace())
	require.Equal(enumspb.RETRY_STATE_NON_RETRYABLE_FAILURE, f.GetChildWorkflowExecutionFailureInfo().GetRetryState())
	require.Equal(fc.ErrorToFailure(applicationErr), f.GetCause())

	err2 := fc.FailureToError(f)
	var childWorkflowExecutionErr *ChildWorkflowExecutionError
	require.True(errors.As(err2, &childWorkflowExecutionErr))
	require.Equal(err.Error(), childWorkflowExecutionErr.Error())
	require.Equal(err.startedEventID, childWorkflowExecutionErr.startedEventID)
}

func Test_convertErrorToFailure_NexusHandlerError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	f := fc.ErrorToFailure(&nexus.HandlerError{
		Type:          nexus.HandlerErrorTypeInternal,
		Cause:         errors.New("custom cause"),
		RetryBehavior: nexus.HandlerErrorRetryBehaviorNonRetryable,
	})
	require.Equal("handler error (INTERNAL): custom cause", f.GetMessage())
	require.Equal(string(nexus.HandlerErrorTypeInternal), f.GetNexusHandlerFailureInfo().Type)
	require.Equal(enumspb.NEXUS_HANDLER_ERROR_RETRY_BEHAVIOR_NON_RETRYABLE, f.GetNexusHandlerFailureInfo().RetryBehavior)
	require.Equal("", f.Cause.GetApplicationFailureInfo().Type)
	require.Equal("custom cause", f.Cause.Message)

	err := fc.FailureToError(f)
	var handlerErr *nexus.HandlerError
	require.ErrorAs(err, &handlerErr)
	require.Equal(nexus.HandlerErrorTypeInternal, handlerErr.Type)
	require.Equal(nexus.HandlerErrorRetryBehaviorNonRetryable, handlerErr.RetryBehavior)
	require.Equal("handler error (INTERNAL): custom cause", handlerErr.Error())

	var applicationErr *ApplicationError
	require.ErrorAs(handlerErr.Cause, &applicationErr)
	require.Equal("custom cause", applicationErr.Error())
}

func Test_convertErrorToFailure_UnknownError(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	err := &coolError{}
	f := fc.ErrorToFailure(err)
	require.Equal("cool error", f.GetMessage())
	require.Equal("coolError", f.GetApplicationFailureInfo().GetType())
	require.Equal(false, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Nil(f.GetCause())

	err2 := fc.FailureToError(f)
	var coolErr *ApplicationError
	require.True(errors.As(err2, &coolErr))
	require.Equal("cool error (type: coolError, retryable: true)", coolErr.Error())
	require.Equal("coolError", coolErr.Type())
}

func Test_convertErrorToFailure_SavedFailure(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()
	err := NewApplicationError("message that will be ignored", "type nobody cares", false, nil)
	var applicationErr *ApplicationError
	require.True(errors.As(err, &applicationErr))

	applicationErr.originalFailure = &failurepb.Failure{
		Message:    "actual message",
		StackTrace: "some stack trace",
		Source:     "JavaSDK",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type:         "SomeJavaException",
			NonRetryable: true,
		}},
	}
	f := fc.ErrorToFailure(err)
	require.Equal("actual message", f.GetMessage())
	require.Equal("JavaSDK", f.GetSource())
	require.Equal("some stack trace", f.GetStackTrace())
	require.Equal("SomeJavaException", f.GetApplicationFailureInfo().GetType())
	require.Equal(true, f.GetApplicationFailureInfo().GetNonRetryable())
	require.Nil(f.GetCause())
}

func Test_convertFailureToError_ApplicationFailure(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()
	details, err := converter.GetDefaultDataConverter().ToPayloads("details", 22)
	assert.NoError(t, err)

	f := &failurepb.Failure{
		Message: "message",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type:         "MyCoolType",
			NonRetryable: true,
			Details:      details,
			Category:     enumspb.APPLICATION_ERROR_CATEGORY_BENIGN,
		}},
		Cause: &failurepb.Failure{
			Message: "cause message",
			FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
				Type:         "UnknownType",
				NonRetryable: false,
			}},
		},
	}

	err = fc.FailureToError(f)
	var applicationErr *ApplicationError
	require.True(errors.As(err, &applicationErr))
	require.Equal("message (type: MyCoolType, retryable: false): cause message (type: UnknownType, retryable: true)", applicationErr.Error())
	require.Equal("MyCoolType", applicationErr.Type())
	require.Equal(true, applicationErr.NonRetryable())
	require.Equal(ApplicationErrorCategoryBenign, applicationErr.Category())
	var str string
	var n int
	require.NoError(applicationErr.Details(&str, &n))
	require.Equal("details", str)
	require.Equal(22, n)

	err = errors.Unwrap(err)
	require.True(errors.As(err, &applicationErr))
	require.Equal("cause message (type: UnknownType, retryable: true)", applicationErr.Error())
	require.Equal("UnknownType", applicationErr.Type())
	require.Equal(false, applicationErr.NonRetryable())

	f = &failurepb.Failure{
		Message:    "message",
		StackTrace: "long stack trace",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type: "PanicError",
		}},
	}

	err = fc.FailureToError(f)
	var panicErr *PanicError
	require.True(errors.As(err, &panicErr))
	require.Equal("message", panicErr.Error())
	require.Equal("long stack trace", panicErr.StackTrace())

	f = &failurepb.Failure{
		Message: "message",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type:     "CoolError",
			Details:  details,
			Category: enumspb.APPLICATION_ERROR_CATEGORY_UNSPECIFIED,
		}},
	}

	err = fc.FailureToError(f)
	var coolErr *ApplicationError
	require.True(errors.As(err, &coolErr))
	require.Equal("message (type: CoolError, retryable: true)", coolErr.Error())
	require.Equal("CoolError", coolErr.Type())
	require.Equal(false, coolErr.NonRetryable())
	require.Equal(ApplicationErrorCategoryUnspecified, coolErr.Category())
}

func Test_convertFailureToError_CanceledFailure(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()

	details, err := converter.GetDefaultDataConverter().ToPayloads("details", 22)
	assert.NoError(t, err)

	f := &failurepb.Failure{
		FailureInfo: &failurepb.Failure_CanceledFailureInfo{CanceledFailureInfo: &failurepb.CanceledFailureInfo{
			Details: details,
		}},
	}

	err = fc.FailureToError(f)
	var canceledErr *CanceledError
	require.True(errors.As(err, &canceledErr))
	var str string
	var n int
	require.NoError(canceledErr.Details(&str, &n))
	require.Equal("details", str)
	require.Equal(22, n)
}

func Test_convertFailureToError_TimeoutFailure(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()
	f := &failurepb.Failure{
		Message: "timeout",
		FailureInfo: &failurepb.Failure_TimeoutFailureInfo{TimeoutFailureInfo: &failurepb.TimeoutFailureInfo{
			TimeoutType:          enumspb.TIMEOUT_TYPE_START_TO_CLOSE,
			LastHeartbeatDetails: nil,
		}},
	}

	err := fc.FailureToError(f)
	var timeoutErr *TimeoutError
	require.True(errors.As(err, &timeoutErr))
	require.Equal("timeout (type: StartToClose)", timeoutErr.Error())
	require.Equal(enumspb.TIMEOUT_TYPE_START_TO_CLOSE, timeoutErr.TimeoutType())
}

func Test_convertFailureToError_ServerFailure(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()
	f := &failurepb.Failure{
		Message: "message",
		FailureInfo: &failurepb.Failure_ServerFailureInfo{ServerFailureInfo: &failurepb.ServerFailureInfo{
			NonRetryable: true,
		}},
	}

	err := fc.FailureToError(f)
	var serverErr *ServerError
	require.True(errors.As(err, &serverErr))
	require.Equal("message", serverErr.Error())
	require.Equal(true, serverErr.nonRetryable)
}

func Test_convertFailureToError_SaveFailure(t *testing.T) {
	require := require.New(t)
	fc := GetDefaultFailureConverter()
	f := &failurepb.Failure{
		Message:    "message",
		StackTrace: "long stack trace",
		Source:     "JavaSDK",
		Cause: &failurepb.Failure{
			Message:    "application message",
			StackTrace: "application long stack trace",
			Source:     "JavaSDK",
			FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
				Type:         "SomeJavaException",
				NonRetryable: true,
			}},
		},
		FailureInfo: &failurepb.Failure_ActivityFailureInfo{ActivityFailureInfo: &failurepb.ActivityFailureInfo{
			StartedEventId:   1,
			ScheduledEventId: 2,
			Identity:         "alex",
		}},
	}

	err := fc.FailureToError(f)

	var applicationErr *ApplicationError
	require.True(errors.As(err, &applicationErr))
	require.NotNil(applicationErr.originalFailure)
	applicationErr.msg = "errors are immutable, message can't be changed"
	applicationErr.errType = "ApplicationError (is ignored)"
	applicationErr.nonRetryable = false

	var activityErr *ActivityError
	require.True(errors.As(err, &activityErr))
	require.NotNil(activityErr.originalFailure)
	activityErr.startedEventID = 11
	activityErr.scheduledEventID = 22
	activityErr.identity = "bob"

	f2 := fc.ErrorToFailure(err)
	require.Equal("message", f2.GetMessage())
	require.Equal("long stack trace", f2.GetStackTrace())
	require.Equal("JavaSDK", f2.GetSource())
	require.Equal(int64(1), f2.GetActivityFailureInfo().GetStartedEventId())
	require.Equal(int64(2), f2.GetActivityFailureInfo().GetScheduledEventId())
	require.Equal("alex", f2.GetActivityFailureInfo().GetIdentity())

	require.Equal("application message", f2.GetCause().GetMessage())
	require.Equal("application long stack trace", f2.GetCause().GetStackTrace())
	require.Equal("JavaSDK", f2.GetCause().GetSource())
	require.Equal("SomeJavaException", f2.GetCause().GetApplicationFailureInfo().GetType())
	require.Equal(true, f2.GetCause().GetApplicationFailureInfo().GetNonRetryable())
}
