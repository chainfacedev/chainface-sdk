package rpc

import "fmt"

const (
	errMsgTimeout          = "request timed out"
	errMsgResponseTooLarge = "response too large"
	errMsgBatchTooLarge    = "batch too large"
)

// HTTPError is returned by client operations when the HTTP status code of the
// response is not a 2xx status.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       []byte
}

func (err HTTPError) Error() string {
	if len(err.Body) == 0 {
		return err.Status
	}
	return fmt.Sprintf("%v: %s", err.Status, err.Body)
}

// Invalid JSON was received by the server.
type parseError struct{ message string }

func (e *parseError) ErrorCode() int { return -32700 }

func (e *parseError) Error() string { return e.message }

// received message isn't a valid request
type invalidRequestError struct{ message string }

func (e *invalidRequestError) ErrorCode() int { return -32600 }

func (e *invalidRequestError) Error() string { return e.message }

// internalServerError is used for server errors during request processing.
type internalServerError struct {
	code    int
	message string
}

func (e *internalServerError) ErrorCode() int { return e.code }

func (e *internalServerError) Error() string { return e.message }

// unable to decode supplied params, or an invalid number of parameters
type invalidParamsError struct{ message string }

func (e *invalidParamsError) ErrorCode() int { return -32602 }

func (e *invalidParamsError) Error() string { return e.message }

type methodNotFoundError struct{ method string }

func (e *methodNotFoundError) ErrorCode() int { return -32601 }

func (e *methodNotFoundError) Error() string {
	return fmt.Sprintf("the method %s does not exist/is not available", e.method)
}

type notificationsUnsupportedError struct{}

func (e notificationsUnsupportedError) Error() string {
	return "notifications not supported"
}

func (e notificationsUnsupportedError) ErrorCode() int { return -32601 }

// Is checks for equivalence to another error. Here we define that all errors with code
// -32601 (method not found) are equivalent to notificationsUnsupportedError. This is
// done to enable the following pattern:
//
//	sub, err := client.Subscribe(...)
//	if errors.Is(err, rpc.ErrNotificationsUnsupported) {
//		// server doesn't support subscriptions
//	}
func (e notificationsUnsupportedError) Is(other error) bool {
	if other == (notificationsUnsupportedError{}) {
		return true
	}
	rpcErr, ok := other.(Error)
	if ok {
		code := rpcErr.ErrorCode()
		return code == -32601 || code == legacyErrcodeNotificationsUnsupported
	}
	return false
}

type subscriptionNotFoundError struct{ namespace, subscription string }

func (e *subscriptionNotFoundError) ErrorCode() int { return -32601 }

func (e *subscriptionNotFoundError) Error() string {
	return fmt.Sprintf("no %q subscription in %s namespace", e.subscription, e.namespace)
}
