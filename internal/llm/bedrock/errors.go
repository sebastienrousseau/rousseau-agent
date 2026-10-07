package bedrock

import (
	"errors"
	"net/http"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"

	"github.com/sebastienrousseau/rousseau-agent/internal/model"
)

// wrapAWSError turns an AWS SDK error into a model.ProviderError
// carrying the HTTP status (throttling is 429, service overload 5xx)
// so the retry wrapper classifies on structure rather than error
// text. Errors raised before a response (credentials, dial) carry
// status 0, which the classifier treats as retryable.
func wrapAWSError(err error) error {
	var re *awshttp.ResponseError
	if errors.As(err, &re) && re.Response != nil {
		var resp *http.Response
		if re.Response.Response != nil {
			resp = re.Response.Response
		}
		return model.NewProviderError(err, re.HTTPStatusCode(), resp)
	}
	return model.NewProviderError(err, 0, nil)
}
