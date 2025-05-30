package test_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	commonpb "go.temporal.io/api/common/v1"

	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/internal"
	bindings "go.temporal.io/sdk/internalbindings"
	"go.temporal.io/sdk/workflow"
)

type EmptyWorkflowDefinitionFactory struct {
}

func (e EmptyWorkflowDefinitionFactory) NewWorkflowDefinition() bindings.WorkflowDefinition {
	return &EmptyWorkflowDefinition{}
}

type EmptyWorkflowDefinition struct {
}

func (wd *EmptyWorkflowDefinition) Execute(env bindings.WorkflowEnvironment, header *commonpb.Header, input *commonpb.Payloads) {
	payload, err := converter.GetDefaultDataConverter().ToPayloads("EmptyResult")
	env.Complete(payload, err)
}

func (wd *EmptyWorkflowDefinition) OnWorkflowTaskStarted(_ time.Duration) {

}

func (wd *EmptyWorkflowDefinition) StackTrace() string {
	return "stackTracePlaceholder"
}

func (wd *EmptyWorkflowDefinition) Close() {

}

type SingleActivityWorkflowDefinitionFactory struct {
}

func (e SingleActivityWorkflowDefinitionFactory) NewWorkflowDefinition() bindings.WorkflowDefinition {
	return &SingleActivityWorkflowDefinition{}
}

type SingleActivityWorkflowDefinition struct {
	callbacks []func()
}

func (d *SingleActivityWorkflowDefinition) Execute(env bindings.WorkflowEnvironment, header *commonpb.Header, input *commonpb.Payloads) {
	var signalInput string
	env.RegisterSignalHandler(func(name string, input *commonpb.Payloads, header *commonpb.Header) error {
		return converter.GetDefaultDataConverter().FromPayloads(input, &signalInput)
	})
	d.callbacks = append(d.callbacks, func() {
		env.NewTimer(time.Second, workflow.TimerOptions{}, d.addCallback(func(result *commonpb.Payloads, err error) {
			input, _ := converter.GetDefaultDataConverter().ToPayloads("World")
			parameters1 := bindings.ExecuteActivityParams{
				ExecuteActivityOptions: bindings.ExecuteActivityOptions{
					TaskQueueName:       env.WorkflowInfo().TaskQueueName,
					StartToCloseTimeout: 10 * time.Second,
					ActivityID:          "id1",
				},
				ActivityType: bindings.ActivityType{Name: "Activity1"},
				Input:        input,
			}
			parameters2 := bindings.ExecuteActivityParams{
				ExecuteActivityOptions: bindings.ExecuteActivityOptions{
					TaskQueueName:       env.WorkflowInfo().TaskQueueName,
					StartToCloseTimeout: 10 * time.Second,
					ActivityID:          "id2",
					RetryPolicy:         &commonpb.RetryPolicy{MaximumAttempts: 1},
				},
				ActivityType: bindings.ActivityType{Name: "ActivityThatFails"},
				Input:        input,
			}
			_ = env.ExecuteActivity(parameters1, d.addCallback(func(result1 *commonpb.Payloads, err error) {
				env.ExecuteActivity(parameters2, d.addCallback(func(result2 *commonpb.Payloads, err error) {
					err = errors.Unwrap(err) // unwrap activity error
					if err == nil {
						env.Complete(nil, errors.New("error expected"))
						return
					}
					failure := internal.GetDefaultFailureConverter().ErrorToFailure(err)
					if failure == nil {
						env.Complete(nil, errors.New("failure expected"))
						return
					}
					if failure.GetApplicationFailureInfo() == nil {
						env.Complete(nil, errors.New("application failure expected"))
						return
					}
					if failure.GetMessage() != err.Error() {
						env.Complete(nil, fmt.Errorf("error message '%v' doesn't match failure message '%v'", err.Error(), failure.GetMessage()))
						return
					}
					childParams := bindings.ExecuteWorkflowParams{
						WorkflowOptions: bindings.WorkflowOptions{
							TaskQueueName: env.WorkflowInfo().TaskQueueName,
							WorkflowID:    "ID1",
						},
						WorkflowType: &bindings.WorkflowType{Name: "ChildWorkflow"},
						Input:        result1,
					}
					env.ExecuteChildWorkflow(childParams, d.addCallback(func(r *commonpb.Payloads, err error) {
						var childResult string
						_ = converter.GetDefaultDataConverter().FromPayloads(r, &childResult)
						result := childResult + signalInput
						encodedResult, _ := converter.GetDefaultDataConverter().ToPayloads(result)
						env.Complete(encodedResult, err)
					}), func(r bindings.WorkflowExecution, e error) {})
				}))
			}))
		}))
	})
}

func (d *SingleActivityWorkflowDefinition) addCallback(callback bindings.ResultHandler) bindings.ResultHandler {
	return func(result *commonpb.Payloads, err error) {
		d.callbacks = append(d.callbacks, func() {
			callback(result, err)
		})
	}
}

func (d *SingleActivityWorkflowDefinition) OnWorkflowTaskStarted(_ time.Duration) {
	for _, callback := range d.callbacks {
		callback()
	}
	d.callbacks = nil
}

func (d *SingleActivityWorkflowDefinition) StackTrace() string {
	panic("Not implemented")
}

func (d *SingleActivityWorkflowDefinition) Close() {
}

func Activity1(ctx context.Context, name string) (string, error) {
	return "Hello " + name, nil
}

func ActivityThatFails(ctx context.Context) error {
	return errors.New("simulated failure")
}

func ChildWorkflow(ctx workflow.Context, greeting string) (string, error) {
	return greeting + "!", nil
}
