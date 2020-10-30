package utils

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/url"
)

const digits = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_="

// ReadMultipartForm 读取 http 多媒体表单
// 同步方法
// (表单名, 文件名) (文件路径, 异常)
// (read buffer, buffer length, read error) 异常
// (表单名, 文件名, 文件路径: open 方法返回的文件路径) 异常
// (文本表单, 文件表单: 值为 open 方法返回的文件路径, 异常)
func ReadMultipartForm(request *http.Request, open func(string, string) (string, error), write func([]byte, int, error) error, close func(string, string, string) error) (url.Values, url.Values, error) {

	r, err := request.MultipartReader()
	if err != nil {
		return nil, nil, err
	}
	defer request.Body.Close()
	value := make(url.Values)
	file := make(url.Values)
	for {
		p, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}

		name := p.FormName()
		if name == "" {
			continue
		}
		filename := p.FileName()

		var b bytes.Buffer

		if filename == "" {
			// value, store as string in memory
			_, err := io.Copy(&b, p)
			if err != nil && err != io.EOF {
				return nil, nil, err
			}
			value[name] = append(value[name], b.String())
			continue
		}

		var fpath string
		fpath, err = open(name, filename)

		if err != nil {
			return nil, nil, err
		}

		buf := make([]byte, 4096) // 4KB, golang http 最大读取长度
		var nr int
		var er error

		for {
			nr, er = p.Read(buf)
			if nr == 0 && io.EOF == er {
				break
			}
			if err = write(buf, nr, er); err != nil {
				break
			}

			if IsDone(request) {
				return nil, nil, errors.New("client done")
			}
		}

		file[name] = append(file[name], fpath)

		err = close(name, filename, fpath)
		if err != nil {
			return nil, nil, err
		}

		if IsDone(request) {
			return nil, nil, errors.New("client done")
		}
	}

	return value, file, err
}

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
