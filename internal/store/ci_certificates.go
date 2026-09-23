package store

import (
	"context"

	"github.com/eutelic/runnerhub/internal/ciaudit"
)

// CIValidationCertificateStore persists successful validation evidence by
// content-addressed validation key. It stays optional so stores that do not
// provide CI analytics do not need to implement it.
type CIValidationCertificateStore interface {
	UpsertCIValidationCertificate(context.Context, string, ciaudit.ValidationCertificate) error
	GetCIValidationCertificate(context.Context, string, string) (ciaudit.ValidationCertificate, error)
}
