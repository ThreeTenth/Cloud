package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	_ "github.com/jinzhu/gorm/dialects/sqlite"
)

const (
	defaultMaxMemory = 512 << 20 // 512 MB
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

func initFlag() {
	flag.StringVar(&downloadPath, "D", "./cloud", "文件存储路径")
	flag.Parse()
	flag.Usage()
}

func initDB() {
	var err error
	sqlite, err = gorm.Open("sqlite3", filepath.Join(downloadPath, "cloud.db"))
	if err != nil {
		panic("failed to connect database: " + err.Error())
	}

	err = sqlite.AutoMigrate(&APK{}).Error

	if nil != err {
		panic(err)
	}
}

func main() {
	initFlag()
	initDB()

	defer sqlite.Close()

	router := gin.Default()
	v1 := router.Group("/v1")

	v1.GET("/apk/:package/:version", output(GetLatestAPK))
	v1.POST("/apk", output(PostAPK))

	router.Run(":19823")
}

func output(fn func(*gin.Context) APIMessage) gin.HandlerFunc {
	return func(c *gin.Context) {
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
