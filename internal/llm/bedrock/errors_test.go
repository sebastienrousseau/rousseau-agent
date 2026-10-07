package bedrock

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

func throttled(retryAfter string) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{retryAfter}},
		}},
		Err: errors.New("ThrottlingException"),
	}}
}

func TestComplete_ThrottlingIsATypedRetryableError(t *testing.T) {
	stub := &stubInvoke{err: throttled("3")}
	p, err := New(context.Background(), Config{Region: "us-west-2", Model: "m", Runtime: stub})
	require.NoError(t, err)
	_, err = p.Complete(context.Background(), model.Request{Messages: []model.Message{model.NewUserText("hi")}})
	require.Error(t, err)

	var pe *model.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, http.StatusTooManyRequests, pe.Status)
	assert.Equal(t, 3*time.Second, pe.RetryAfter)
	assert.True(t, model.Retryable(err))
	assert.Contains(t, err.Error(), "bedrock: invoke")
}

func TestWrapAWSError_NonHTTPErrorsCarryStatusZero(t *testing.T) {
	err := wrapAWSError(errors.New("no credentials"))
	var pe *model.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Zero(t, pe.Status)
	assert.True(t, model.Retryable(err))

	// A ResponseError without a response body still wraps safely.
	bare := &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{Err: errors.New("x")}}
	err = wrapAWSError(bare)
	require.ErrorAs(t, err, &pe)
	assert.Zero(t, pe.Status)
}
