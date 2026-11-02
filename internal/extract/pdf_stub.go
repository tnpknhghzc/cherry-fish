//go:build nopdf

package extract

import (
	"context"
	"errors"
)

// Сборка с тегом nopdf: PDF-ридер (MuPDF, AGPL) не включён.
func loadPDF(ctx context.Context, path string, prog Progress) (*Document, error) {
	return nil, errors.New("эта сборка программы не содержит чтения PDF (собрана с тегом nopdf); откройте книгу в DOCX/ODT/EPUB/FB2/TXT или используйте полную сборку")
}
