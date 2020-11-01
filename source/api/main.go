package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"time"

	// "github.com/gin-contrib/pprof"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"cloud.saynice.xyz/utils"

	_ "github.com/jinzhu/gorm/dialects/sqlite"
)

const indexHTMLTemplate = `<!DOCTYPE HTML>
<html>
<head>
  <meta charset="utf-8">
  <title>Drop files here or click to upload</title>
	<link rel="stylesheet" href="https://cdn.jsdelivr.net/combine/npm/dropzone@5.7.2/dist/basic.min.css,npm/dropzone@5.7.2/dist/dropzone.min.css">
	<script src="https://cdn.jsdelivr.net/npm/dropzone@5.7.2/dist/dropzone.min.js"></script>
</head>
<body>
<form action="/v1/file" class="dropzone" style="border: 2px dashed #0087F7;">
		<div class="dz-message needsclick">
    <h3>Drop files here or click to upload.</h3>
    <dd>This is just a demo. Selected files are <strong>not</strong> actually uploaded.</dd>
	  </div>
</form>
  <script>
    document.onclick = function (e) {
      var e = e ? e : window.event;
      var tar = e.srcElement || e.target;
			var cls = tar.parentElement ? tar.parentElement.className : tar.className;
			if ("dz-filename" == cls) { window.open("/v1/file/"+tar.innerText); }
    } 
  </script>
</body>
</html>
`

const (
	defaultMaxMemory = 16 << 20 //  16 MB
	_1M              = 1 << 20  // 1MB
)

var (
	sqlite       *gorm.DB
	downloadPath string
)

// APIMessage API 消息体
type APIMessage struct {
	Code int         `json:"code"`
	Type string      `json:"-"`
	Data interface{} `json:"data"`
}

func (msg *APIMessage) Error() string {
	return fmt.Sprintf("%v", msg.Data)
}

func getStringAPIMessage(code int, data string) APIMessage {
	return APIMessage{Code: code, Type: "string", Data: data}
}

func getJSONAPIMessage(code int, data interface{}) APIMessage {
	return APIMessage{Code: code, Type: "json", Data: data}
}

func getBinaryAPIMessage(code int, data string) APIMessage {
	return APIMessage{Code: code, Type: "binary", Data: data}
}

// APK APK信息
type APK struct {
	gorm.Model
	PackageName string `gorm:"type:text;default:'';not null;"`
	VersionCode int    `gorm:"type:integer;default:0;not null;"`
	VersionName string `gorm:"type:text;default:'';not null;"`
	FileURL     string `gorm:"type:text;default:'';not null;"`
}

// Storage 存储对象
type Storage struct {
	gorm.Model
	Md5    string `gorm:"type:text;not null;"`
	Path   string `gorm:"type:text;not null;"`
	Name   string `gorm:"type:text;not null;"`
	Length int64  `gorm:"type:integer;default:0;not null;"`
	Type   string `gorm:"type:text;default:'application/octet-stream';not null;"`
}

// IndexHTML Cloud 试用首页
func IndexHTML(c *gin.Context) {
	tmpl, err := template.New("index").Parse(indexHTMLTemplate)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}

	c.Status(200)
	tmpl.Execute(c.Writer, nil)
}

func output(fn func(*gin.Context) APIMessage) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ch := make(chan APIMessage, 1)
		// go func() {
		// 	ch <- fn(c)
		// }()

		// var msg APIMessage
		// select {
		// case msg = <-ch:
		// 	fmt.Println(msg)
		// case <-c.Request.Context().Done():
		// 	fmt.Println("Client done")
		// 	return
		// }
		msg := fn(c)

		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, accept, origin, Cache-Control, X-Requested-With")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT")

		switch msg.Type {
		case "string":
			c.String(msg.Code, msg.Data.(string))
		case "json":
			c.JSON(msg.Code, msg.Data)
		case "binary":
			c.File(msg.Data.(string))
		}
	}
}

func checkMultipartForm(c *gin.Context) error {
	if c.Request.MultipartForm == nil {
		e := c.Request.ParseMultipartForm(0)
		return e
	}
	return nil
}

// GetFile 获取指定文件
func GetFile(c *gin.Context) APIMessage {
	filename := c.Param("name")

	var storage Storage
	ef := sqlite.Where("name=?", filename).Find(&storage).Error
	if ef != nil {
		return getStringAPIMessage(http.StatusNotFound, ef.Error())
	}

	_, e := os.Stat(storage.Path)

	if e != nil {
		return getStringAPIMessage(http.StatusNotFound, "The system cannot find the file specified.")
	}

	// 指定浏览器直接打开文件，不进行下载操作
	c.Header("Content-Disposition", "inline; filename="+filename)
	// 指定浏览器直接下载文件，不进行打开操作
	// c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", storage.Type)

	return getBinaryAPIMessage(http.StatusOK, storage.Path)
}

// PostFile 保存文件
func PostFile(c *gin.Context) APIMessage {
	fname, _ := c.Params.Get("name")

	_, _, err := SaveMultipartFile(c.Request, fname)

	if err != nil {
		return getStringAPIMessage(http.StatusInternalServerError, err.Error())
	}

	return getStringAPIMessage(http.StatusOK, "success")
}

// PostFiles 保存多个文件
func PostFiles(c *gin.Context) APIMessage {
	_, _, err := SaveMultipartFile(c.Request)

	if err != nil {
		return getStringAPIMessage(http.StatusInternalServerError, err.Error())
	}

	return getStringAPIMessage(http.StatusOK, "success")
}

// PostURI 提交一个 URI，进行离线下载
func PostURI(c *gin.Context) APIMessage {
	dl, ok := c.Params.Get("url")
	if !ok && len(dl) <= 1 {
		return getStringAPIMessage(http.StatusBadRequest, "This uri is invalid")
	}

	req, err := http.NewRequest("GET", dl[1:], nil)
	if err != nil {
		return getStringAPIMessage(http.StatusNotFound, "This uri can't find")
	}

	resp, ed := http.DefaultClient.Do(req)

	if ed != nil {
		return getStringAPIMessage(http.StatusBadRequest, "This uri is invalid")
	}

	defer resp.Body.Close()

	disp := resp.Header.Get("Content-Disposition")
	var filename string
	_, params, ep := mime.ParseMediaType(disp)
	if ep != nil {
		filename = path.Base(req.URL.Path)
	} else {
		filename = params["filename"]
	}

	open := func() (io.ReadCloser, error) { return resp.Body, nil }
	_, id, es := saveFile(filename, open)

	if es != nil {
		return getStringAPIMessage(http.StatusInternalServerError, es.Error())
	}

	return getJSONAPIMessage(http.StatusOK, id)
}

// SaveMultipartFile 保存多文件
func SaveMultipartFile(request *http.Request, rename ...string) (url.Values, url.Values, error) {
	var tempfile *os.File
	var err error
	var fname string

	if 1 == len(rename) {
		fname = rename[0]
	}

	prevTime := time.Now().Unix()

	open := func(name string, filename string) (string, error) {
		if "file" != name {
			return "", errors.New("Params error: not support " + name)
		}
		if fname == "" {
			fname = filename
		}
		path := filepath.Join(temp(), filename)
		tempfile, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
		return path, err
	}

	h := md5.New()
	var len int64
	var nw int
	var ew error

	write := func(buf []byte, nr int, er error) error {
		if nr > 0 {
			// 计算文件 md5 值
			nw, ew = h.Write(buf[:nr])
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}

			// 储存临时文件
			nw, ew = tempfile.Write(buf[:nr])
			if ew != nil {
				return ew
			}
			if nr != nw {
				return io.ErrShortWrite
			}

			len += int64(nw)
		}
		return er
	}

	close := func(name string, filename string, path string) error {
		// 不要在可写文件上使用 defer Close() 方法
		// @see https://www.joeshaw.org/dont-defer-close-on-writable-files/
		if err1 := tempfile.Close(); err == nil {
			err = err1
		}

		if err != nil {
			os.Remove(path)
			return err
		}

		md5str := hex.EncodeToString(h.Sum(nil))
		filedir := filepath.Join(downloadPath, time.Now().Format("20060102"))
		em := MkdirIfNotExists(filedir)
		if em != nil {
			os.Remove(path)
			fmt.Println(em.Error())
			return em
		}

		var storage Storage
		var filePath string
		ef := sqlite.Where("md5=?", md5str).First(&storage).Error
		if ef != nil {
			filePath = filepath.Join(filedir, md5str)
			er := os.Rename(path, filePath)
			if er != nil {
				os.Remove(path)
				return er
			}
		} else {
			filePath = storage.Path
			os.Remove(path)
		}

		contentType, _ := GetFileContentType(filePath)

		save := Storage{
			Md5:    md5str,
			Path:   filePath,
			Name:   fname,
			Length: len,
			Type:   contentType,
		}

		ess := sqlite.Save(&save).Error
		if ess != nil {
		}
		fmt.Println(">>>", fname, ":", float64(len)/1048576.0, float64(len)/float64(time.Now().Unix()-prevTime)/1048576.0)
		return ess
	}

	return utils.ReadMultipartForm(request, open, write, close)
}

func saveFile(filename string, open func() (io.ReadCloser, error)) (string, uint, error) {
	file, eo := open()

	fmt.Println(time.Now(), "open file")

	if eo != nil {
		return filename, 0, eo
	}
	defer file.Close()

	tempPath := filepath.Join(temp(), filename)

	_, es := os.Stat(tempPath)
	if es == nil {
		return filename, 0, errors.New("File already exists")
	}

	temp, eof := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
	if eof != nil {
		return filename, 0, eof
	}
	fmt.Println(time.Now(), "create temp")

	h := md5.New()
	buf := make([]byte, 32*1024) // 32KB buffer
	var len int64 = 0
	var err error
	var nr, nw int
	var er, ew error

	for {
		nr, er = file.Read(buf)

		if nr > 0 {
			// 计算文件 md5 值
			nw, ew = h.Write(buf[:nr])
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}

			// 储存临时文件
			nw, ew = temp.Write(buf[:nr])
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
			len += int64(nw)
		}
		// fmt.Println(time.Now(), "download ", len, nr)
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}

	fmt.Println(time.Now(), "finish ", len, nr)

	if err1 := temp.Close(); err == nil {
		err = err1
	}

	if err != nil {
		os.Remove(tempPath)
		fmt.Println(err.Error())
		return filename, 0, err
	}

	md5str := hex.EncodeToString(h.Sum(nil))
	filedir := filepath.Join(downloadPath, time.Now().Format("20060102"))
	em := MkdirIfNotExists(filedir)
	if em != nil {
		os.Remove(tempPath)
		fmt.Println(em.Error())
		return filename, 0, em
	}

	var storage Storage
	var filePath string
	ef := sqlite.Where("md5=?", md5str).First(&storage).Error
	if ef != nil {
		filePath = filepath.Join(filedir, md5str)
		er := os.Rename(tempPath, filePath)
		if er != nil {
			os.Remove(tempPath)
			return filename, 0, er
		}
	} else {
		filePath = storage.Path
		os.Remove(tempPath)
	}

	contentType, _ := GetFileContentType(tempPath)

	save := Storage{
		Md5:    md5str,
		Path:   filePath,
		Name:   filename,
		Length: len,
		Type:   contentType,
	}

	ess := sqlite.Save(&save).Error
	if ess != nil {
		return filename, 0, ess
	}
	return filename, save.ID, nil
}

// GetLatestAPK 根据包名和版本号，获取最新的 APK 文件
func GetLatestAPK(c *gin.Context) APIMessage {
	packageName := c.Param("package")
	versionCode, e := strconv.Atoi(c.Param("version"))

	if e != nil {
		return getStringAPIMessage(http.StatusBadRequest, e.Error())
	}

	apk := APK{}

	e = sqlite.Where("package_name=?", packageName).Order("version_code desc").First(&apk).Error

	if e != nil {
		return getStringAPIMessage(http.StatusNotFound, e.Error())
	}

	if apk.VersionCode <= versionCode {
		return getStringAPIMessage(http.StatusForbidden, "Already the latest version")
	}

	fi, e := os.Stat(apk.FileURL)

	if e != nil {
		return getStringAPIMessage(http.StatusGone, e.Error())
	}

	filename := apk.PackageName + "_" + apk.VersionName + "_" + strconv.Itoa(apk.VersionCode) + ".apk"

	c.Writer.WriteHeader(http.StatusOK)
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Accept-Length", fmt.Sprintf("%d", fi.Size()))

	return getBinaryAPIMessage(http.StatusOK, apk.FileURL)
}

// PostAPK 提交一个或一组 APK
// 首先保存APK为临时文件
// 解析APK，判断相同版本相同包名的是否已经入库
// 如果未入库，则直接保存入库，并修改临时文件为正式文件
// 如果已入库，则抛弃
func PostAPK(c *gin.Context) APIMessage {
	if c.Request.MultipartForm == nil {
		e := c.Request.ParseMultipartForm(defaultMaxMemory)
		if e != nil {
			return getStringAPIMessage(http.StatusRequestEntityTooLarge, e.Error())
		}
	}

	if c.Request.MultipartForm != nil && c.Request.MultipartForm.File != nil {
		status := make(map[string]string)
		if files := c.Request.MultipartForm.File["file"]; len(files) > 0 {
			for _, v := range files {
				file, e := v.Open()

				if e != nil {
					status[v.Filename] = e.Error()
					continue
				}
				defer file.Close()

				bytes, e := ioutil.ReadAll(file)

				if e != nil {
					status[v.Filename] = e.Error()
					continue
				}

				filename := v.Filename

				e = SaveAPK(filename, bytes)

				if e != nil {
					if msg, ok := e.(*APIMessage); ok {
						return *msg
					}

					status[filename] = e.Error()
					continue
				}
			}

			return getJSONAPIMessage(http.StatusOK, status)
		}
	}

	return getStringAPIMessage(http.StatusBadRequest, "http: no such file")
}

// SaveAPK 保存文件
func SaveAPK(filename string, content []byte) error {
	tempFilePath := filename
	e := ioutil.WriteFile(tempFilePath, content, 0666)

	if e != nil {
		return e
	}

	apkInfoBytes, e := cmd("aapt2", "dump", "badging", tempFilePath)
	apkInfo := string(apkInfoBytes)

	if e != nil {
		os.Remove(tempFilePath)
		return errors.New(e.Error() + ": " + apkInfo)
	}

	apkPackageRegexp := regexp.MustCompile(`package: name.*`)
	if apkPackageRegexp.MatchString(apkInfo) {
		apkPackageInfo := apkPackageRegexp.Find([]byte(apkInfo))
		packageInfo := strings.Split(string(apkPackageInfo[9:]), " ")
		apk := APK{}
		for _, v := range packageInfo {
			info := strings.Split(v, "=")
			name := info[0]
			value := info[1]
			value = value[1 : len(value)-1]
			if "name" == name {
				apk.PackageName = value
			} else if "versionName" == name {
				apk.VersionName = value
			} else if "versionCode" == name {
				versionCode, _ := strconv.Atoi(value)
				apk.VersionCode = versionCode
			}
		}

		count := 0

		sqlite.Model(&APK{}).Where(apk).Count(&count)

		if 0 < count {
			os.Remove(tempFilePath)
			return errors.New("Error: An application with the same package name and version number already exists")
		}

		saveFilePath := filepath.Join(downloadPath, filename)
		e := os.Rename(tempFilePath, saveFilePath)

		if e != nil {
			os.Remove(tempFilePath)
			return e
		}

		apk.FileURL = saveFilePath
		e = sqlite.Set("gorm:association_autoupdate", false).Create(&apk).Error

		if e != nil {
			os.Remove(saveFilePath)
			return e
		}

		return nil
	}

	os.Remove(tempFilePath)

	return errors.New("Error: File isn't apk file")
}

func cmd(name string, arg ...string) ([]byte, error) {
	cmd := exec.Command(name, arg...)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	e := cmd.Run()
	if e != nil {
		return stderr.Bytes(), e
	}

	return out.Bytes(), nil
}

func preNUm(data byte) int {
	var mask byte = 0x80
	var num int = 0
	//8bit中首个0bit前有多少个1bits
	for i := 0; i < 8; i++ {
		if (data & mask) == mask {
			num++
			mask = mask >> 1
		} else {
			break
		}
	}
	return num
}

func isUtf8(data []byte) bool {
	i := 0
	for i < len(data) {
		if (data[i] & 0x80) == 0x00 {
			// 0XXX_XXXX
			i++
			continue
		} else if num := preNUm(data[i]); num > 2 {
			// 110X_XXXX 10XX_XXXX
			// 1110_XXXX 10XX_XXXX 10XX_XXXX
			// 1111_0XXX 10XX_XXXX 10XX_XXXX 10XX_XXXX
			// 1111_10XX 10XX_XXXX 10XX_XXXX 10XX_XXXX 10XX_XXXX
			// 1111_110X 10XX_XXXX 10XX_XXXX 10XX_XXXX 10XX_XXXX 10XX_XXXX
			// preNUm() 返回首个字节的8个bits中首个0bit前面1bit的个数，该数量也是该字符所使用的字节数
			i++
			for j := 0; j < num-1; j++ {
				//判断后面的 num - 1 个字节是不是都是10开头
				if (data[i] & 0xc0) != 0x80 {
					return false
				}
				i++
			}
		} else {
			//其他情况说明不是utf-8
			return false
		}
	}
	return true
}

func isGBK(data []byte) bool {
	length := len(data)
	var i int = 0
	for i < length {
		if data[i] <= 0x7f {
			//编码0~127,只有一个字节的编码，兼容ASCII码
			i++
			continue
		} else {
			//大于127的使用双字节编码，落在gbk编码范围内的字符
			if data[i] >= 0x81 &&
				data[i] <= 0xfe &&
				data[i+1] >= 0x40 &&
				data[i+1] <= 0xfe &&
				data[i+1] != 0xf7 {
				i += 2
				continue
			} else {
				return false
			}
		}
	}
	return true
}

func gbkToUtf8(s []byte) ([]byte, error) {
	reader := transform.NewReader(bytes.NewReader(s), simplifiedchinese.GBK.NewDecoder())
	d, err := ioutil.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// GetFileContentType 获取文件格式
// fs.go serveContent()#ctypes 变量获取方法
// 又见：https://golangcode.com/get-the-content-type-of-file/
func GetFileContentType(filePath string) (string, error) {
	ctype := mime.TypeByExtension(filepath.Ext(filePath))
	if ctype == "" {
		const defaultType = "application/octet-stream"
		// The algorithm uses at most sniffLen bytes to make its decision.
		const sniffLen = 512
		// read a chunk to decide between utf-8 text and binary
		var buf [sniffLen]byte
		content, eo := os.Open(filePath)
		if eo != nil {
			return defaultType, eo
		}
		defer content.Close()

		n, er := io.ReadFull(content, buf[:])
		if er != nil {
			return defaultType, er
		}
		ctype = http.DetectContentType(buf[:n])
	}

	return ctype, nil
}

func temp() string {
	return filepath.Join(downloadPath, ".temp")
}

// MkdirIfNotExists 如果目录不存在则创建
func MkdirIfNotExists(path string) error {
	_, err := os.Stat(path)

	if err != nil && os.IsNotExist(err) {
		err = os.Mkdir(path, 0666)
	}

	return err
}

func initFlag() {
	flag.StringVar(&downloadPath, "D", "./cloud", "文件存储路径")
	flag.Parse()
	flag.Usage()
}

func initStorage() {
	err := MkdirIfNotExists(temp())
	if err != nil {
		panic("failed to create temp dir: " + err.Error())
	}
}

func initDB() {
	var err error
	sqlite, err = gorm.Open("sqlite3", filepath.Join(downloadPath, "cloud.db"))
	if err != nil {
		panic("failed to connect database: " + err.Error())
	}

	err = sqlite.AutoMigrate(&APK{}).Error
	err = sqlite.AutoMigrate(&Storage{}).Error

	if nil != err {
		panic(err)
	}
}

func main() {
	initFlag()
	initStorage()
	initDB()

	defer sqlite.Close()

	router := gin.Default()
	router.GET("/", IndexHTML)

	v1 := router.Group("/v1")

	v1.GET("/file/:name", output(GetFile))
	v1.POST("/file", output(PostFile))
	v1.POST("/file/:name", output(PostFile))
	v1.POST("/files", output(PostFiles))
	v1.GET("/dl/*url", output(PostURI))
	v1.GET("/apk/:package/:version", output(GetLatestAPK))
	v1.POST("/apk", output(PostAPK))

	router.Run(":19823")
}
