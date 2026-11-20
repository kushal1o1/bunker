package backup

import (
	"net/http"

	"github.com/kushal1o1/bunker/internal/model"
)

// NewS3StoreForTest lets tests reach a fake S3 over TLS with its own
// certificate. Only compiled into tests.
func NewS3StoreForTest(spec model.S3Spec, transport http.RoundTripper) (*S3Store, error) {
	return newS3Store(spec, transport)
}
