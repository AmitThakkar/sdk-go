package converter

import (
	"context"
	"fmt"
	"log"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/failure/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var zlibDataConverter = NewCodecDataConverter(
	defaultDataConverter,
	NewZlibCodec(ZlibCodecOptions{AlwaysEncode: true}),
)

func unencodedPayloads() *commonpb.Payloads {
	p, _ := defaultDataConverter.ToPayloads("test")
	return p
}

func encodedPayloads() *commonpb.Payloads {
	p, _ := zlibDataConverter.ToPayloads("test")
	return p
}

func payloadEncoding(payloads *commonpb.Payloads) string {
	return string(payloads.Payloads[0].Metadata[MetadataEncoding])
}

func TestPayloadCodecGRPCClientInterceptor(t *testing.T) {
	require := require.New(t)

	server, err := startTestGRPCServer()
	require.NoError(err)

	interceptor, err := NewPayloadCodecGRPCClientInterceptor(
		PayloadCodecGRPCClientInterceptorOptions{
			Codecs: []PayloadCodec{NewZlibCodec(ZlibCodecOptions{AlwaysEncode: true})},
		},
	)
	require.NoError(err)

	c, err := grpc.NewClient(
		server.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(interceptor),
	)
	require.NoError(err)

	client := workflowservice.NewWorkflowServiceClient(c)

	_, err = client.StartWorkflowExecution(
		context.Background(),
		&workflowservice.StartWorkflowExecutionRequest{
			Input: unencodedPayloads(),
		},
	)
	require.NoError(err)

	require.Equal("binary/zlib", payloadEncoding(server.startWorkflowExecutionRequest.Input))

	response, err := client.PollActivityTaskQueue(
		context.Background(),
		&workflowservice.PollActivityTaskQueueRequest{},
	)
	require.NoError(err)

	require.Equal("json/plain", payloadEncoding(response.Input))
}

func TestFailureGRPCClientInterceptor(t *testing.T) {
	require := require.New(t)

	server, err := startTestGRPCServer()
	require.NoError(err)

	interceptor, err := NewFailureGRPCClientInterceptor(
		NewFailureGRPCClientInterceptorOptions{
			EncodeCommonAttributes: true,
		},
	)
	require.NoError(err)

	c, err := grpc.NewClient(
		server.addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithChainUnaryInterceptor(interceptor),
	)
	require.NoError(err)

	client := workflowservice.NewWorkflowServiceClient(c)

	_, err = client.RespondWorkflowTaskFailed(
		context.Background(),
		&workflowservice.RespondWorkflowTaskFailedRequest{
			Failure: &failure.Failure{
				Message:    "internal error: code 123",
				StackTrace: "internal_file:12",
			},
		},
	)
	require.NoError(err)

	require.Equal("Encoded failure", server.respondWorkflowTaskFailedRequest.Failure.Message)
	require.Equal("", server.respondWorkflowTaskFailedRequest.Failure.StackTrace)

	res, err := client.PollWorkflowTaskQueue(
		context.Background(),
		&workflowservice.PollWorkflowTaskQueueRequest{},
	)
	require.NoError(err)

	attrs, ok := res.History.Events[0].Attributes.(*history.HistoryEvent_ChildWorkflowExecutionFailedEventAttributes)
	require.True(ok)
	f := attrs.ChildWorkflowExecutionFailedEventAttributes.Failure

	require.Equal("internal error: code 123", f.Message)
	require.Equal("internal_file:12", f.StackTrace)
}

type testGRPCServer struct {
	workflowservice.UnimplementedWorkflowServiceServer
	*grpc.Server
	addr                             string
	startWorkflowExecutionRequest    *workflowservice.StartWorkflowExecutionRequest
	respondWorkflowTaskFailedRequest *workflowservice.RespondWorkflowTaskFailedRequest
}

func startTestGRPCServer() (*testGRPCServer, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	t := &testGRPCServer{Server: grpc.NewServer(), addr: l.Addr().String()}
	workflowservice.RegisterWorkflowServiceServer(t.Server, t)
	go func() {
		if err := t.Serve(l); err != nil {
			log.Fatal(err)
		}
	}()

	// Wait until get-system-info reports serving
	return t, t.waitUntilServing()
}

func (t *testGRPCServer) waitUntilServing() error {
	// Try 20 times, waiting 100ms between
	var lastErr error
	for i := 0; i < 20; i++ {
		conn, err := grpc.NewClient(t.addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			lastErr = err
		} else {
			_, err := workflowservice.NewWorkflowServiceClient(conn).GetClusterInfo(
				context.Background(),
				&workflowservice.GetClusterInfoRequest{},
			)
			_ = conn.Close()
			if err != nil {
				lastErr = err
			} else {
				return nil
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("failed waiting, last error: %w", lastErr)
}

func (t *testGRPCServer) GetClusterInfo(
	context.Context,
	*workflowservice.GetClusterInfoRequest,
) (*workflowservice.GetClusterInfoResponse, error) {
	return &workflowservice.GetClusterInfoResponse{}, nil
}

func (t *testGRPCServer) StartWorkflowExecution(
	ctx context.Context,
	req *workflowservice.StartWorkflowExecutionRequest,
) (*workflowservice.StartWorkflowExecutionResponse, error) {
	t.startWorkflowExecutionRequest = req
	return &workflowservice.StartWorkflowExecutionResponse{}, nil
}

func (t *testGRPCServer) RespondWorkflowTaskFailed(
	ctx context.Context,
	req *workflowservice.RespondWorkflowTaskFailedRequest,
) (*workflowservice.RespondWorkflowTaskFailedResponse, error) {
	t.respondWorkflowTaskFailedRequest = req
	return &workflowservice.RespondWorkflowTaskFailedResponse{}, nil
}

func (t *testGRPCServer) PollWorkflowTaskQueue(
	ctx context.Context,
	req *workflowservice.PollWorkflowTaskQueueRequest,
) (*workflowservice.PollWorkflowTaskQueueResponse, error) {
	f := failure.Failure{
		Message:    "internal error: code 123",
		StackTrace: "internal_file:12",
	}
	err := EncodeCommonFailureAttributes(GetDefaultDataConverter(), &f)
	if err != nil {
		return nil, err
	}

	return &workflowservice.PollWorkflowTaskQueueResponse{
		History: &history.History{
			Events: []*history.HistoryEvent{
				{
					EventType: enums.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_FAILED,
					Attributes: &history.HistoryEvent_ChildWorkflowExecutionFailedEventAttributes{
						ChildWorkflowExecutionFailedEventAttributes: &history.ChildWorkflowExecutionFailedEventAttributes{
							Failure: &f,
						},
					},
				},
			},
		},
	}, nil
}

func (t *testGRPCServer) PollActivityTaskQueue(
	ctx context.Context,
	req *workflowservice.PollActivityTaskQueueRequest,
) (*workflowservice.PollActivityTaskQueueResponse, error) {
	return &workflowservice.PollActivityTaskQueueResponse{
		Input: encodedPayloads(),
	}, nil
}
