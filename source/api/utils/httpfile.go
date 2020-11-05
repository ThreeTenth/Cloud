package utils

import (
	"crypto/rand"
	"io"
	"net/http"
)

const digits = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_="

// IsDone http request is done
func IsDone(r *http.Request) bool {
	select {
	case <-r.Context().Done():
		return true
	default:
		return false
	}
}

// New64HexUUID 获取 64进制 UUID, 10个数字+26个小写字母+26个大写字母+(=、_), 长度 24
// generates a random UUID according to RFC 4122
func New64HexUUID() (string, error) {
	uuid := make([]byte, 18)
	n, err := io.ReadFull(rand.Reader, uuid)
	if n != len(uuid) || err != nil {
		return "", err
	}
	// variant bits; see section 4.1.1
	uuid[8] = uuid[8]&^0xc0 | 0x80
	// version 4 (pseudo-random); see section 4.1.3
	uuid[6] = uuid[6]&^0xf0 | 0x40

	return uuidValueOf(uuid), nil
}

func uuidValueOf(b []byte) string {
	carry := 6
	size := 3

	var temp []byte
	var s [68]byte
	sindex := 0
	var start, end int

	for {
		if start == len(b) {
			break
		}
		end = start + size
		if len(b) < end {
			end = len(b)
		}
		temp = b[start:end]
		start = end
		u := uint64(temp[len(temp)-1])
		ucarry := 8
		for i := len(temp) - 2; 0 <= i; i-- {
			u |= uint64(temp[i]) << ucarry
			ucarry += 8
		}
		i := 4
		for {
			if u <= 0 {
				break
			}

			i--
			s[sindex+i] = digits[u&63]
			u >>= carry
		}
		for 0 < i {
			i--
			s[sindex+i] = digits[0]
		}
		sindex += 4
	}
	return string(s[0:sindex])
}
