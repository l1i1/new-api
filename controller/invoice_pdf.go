/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// invoiceFilenameRegexp keeps the request-local attachment name safe for MIME
// headers. The PDF itself is sent immediately and never stored by this service.
var invoiceFilenameRegexp = regexp.MustCompile(`[^A-Za-z0-9._\-]`)

var errInvoiceUploadTooLarge = errors.New("invoice upload exceeds size limit")

// sanitizePDFFilename cleans the administrator-provided filename for use as the
// immediate email attachment name.
func sanitizePDFFilename(filename string) string {
	name := strings.TrimSpace(filename)
	if name == "" {
		return "invoice.pdf"
	}
	name = invoiceFilenameRegexp.ReplaceAllString(name, "_")
	if len(name) > 180 {
		name = name[len(name)-180:]
	}
	if !strings.HasSuffix(strings.ToLower(name), ".pdf") {
		name += ".pdf"
	}
	return name
}

// looksLikePDF validates the file magic bytes without trusting the content-type
// header supplied by the client.
func looksLikePDF(data []byte) bool {
	return len(data) >= 4 && bytes.Equal(data[:4], []byte("%PDF"))
}

// readUploadedFile reads a request-local multipart part up to maxBytes and
// never creates a temporary file.
func readUploadedFile(reader io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", errInvoiceUploadTooLarge, len(data))
	}
	return data, nil
}
