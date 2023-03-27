package readers

import "errors"

type BadReader struct{}

func (r *BadReader) Read([]byte) (int, error) {
	return 0, errors.New("bad reader")
}
