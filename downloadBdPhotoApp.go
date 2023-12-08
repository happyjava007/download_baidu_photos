package main

import (
	"bufio"
	json2 "encoding/json"
	"github.com/bitly/go-simplejson"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var http_client = http.DefaultClient

type ImageItem struct {
	Md5      string `json:"md5"`
	Fsid     string `json:"fsid"`
	Filename string `json:"filename"`
	Ctime    int64  `json:"ctime"`
}

var cookieFilename = "cookie.txt"
var imageListFilename = "imageList.json"
var downloadFilename = "download.txt"

var saveMd5Lock sync.Mutex

// 并发数
var concurrence_size = 10

func main() {
	// 检查文件
	checkFile(cookieFilename)
	checkFile(imageListFilename)
	checkFile(downloadFilename)

	bytes, err := os.ReadFile(cookieFilename)
	if err != nil {
		log.Fatalln("读取cookie文件异常", err)
	}

	var cookie = string(bytes)
	if len(cookie) == 0 {
		log.Fatalln("请输入cookie至cookie.txt文件")
	}

	log.Println("你的cookie:" + cookie)
	log.Println("开始获取你的图片列表=======")

	// 获取所有图片列表
	image_list := get_image_list(cookie)
	log.Println("获取图片列表成功，开始下载图片")

	imageDownDir := "./images/"
	if _, err := os.Stat(imageDownDir); os.IsNotExist(err) {
		if err := os.Mkdir(imageDownDir, 0755); err != nil {
			log.Println(err)
			return
		}
		log.Println("目录已创建")
	}

	download_iamge(cookie, image_list)
}

// 获取图片列表
func get_image_list(cookie string) []ImageItem {
	var image_list []ImageItem

	bytes, _ := os.ReadFile(imageListFilename)
	if bytes != nil {
		err := json2.Unmarshal(bytes, &image_list)
		if err == nil {
			return image_list
		}
	}

	cursor := ""
	for {
		json, err := get_images(cursor, cookie)
		if err != nil {
			log.Fatalln("获取图片列表异常", err)
		} else {
			errno, err := json.Get("errno").Int()
			if err != nil {
				log.Fatalln("获取图片列表异常", err)
			} else {
				if errno != 0 {
					// 失败了
					log.Fatalln("获取图片列表异常", json)
				} else {
					// 成功了
					// 判断是不是没有更多了

					array, err := json.Get("list").Array()
					if err != nil {
						log.Fatalln("获取图片列表异常", json)
					} else {
						for _, data := range array {
							if itemMap, ok := data.(map[string]interface{}); ok {
								fsid := itemMap["fsid"]
								md5 := itemMap["md5"]
								fsidStr := fsid.(json2.Number)
								md5Str := md5.(string)
								path := itemMap["path"]
								pathStr := path.(string)
								split := strings.Split(pathStr, "/")
								filename := split[len(split)-1]
								ctime, _ := itemMap["ctime"].(json2.Number).Int64()

								if len(fsidStr) > 0 {
									image_list = append(image_list, ImageItem{Md5: md5Str, Fsid: fsidStr.String(), Filename: filename, Ctime: ctime})
								}
							}
						}
						log.Println("已发现" + strconv.Itoa(len(image_list)) + "张图片")
					}

					// 判断是不是还有更多
					has_more, err := json.Get("has_more").Int()
					if err != nil {
						log.Fatalln("获取图片列表异常", json)
					} else {
						if has_more != 1 {
							// 没有更多了
							// 写入本地文件
							bytes, err := json2.Marshal(image_list)
							if err != nil {
								log.Fatalln("获取图片列表异常", json)
							} else {
								err := os.WriteFile(imageListFilename, bytes, os.ModePerm)
								if err != nil {
									log.Fatalln("获取图片列表异常", json)
								}
								log.Println("没有更多图片了")
								return image_list
							}
						} else {
							// 还有更多图片
							cursor, err = json.Get("cursor").String()
							if err != nil {
								log.Fatalln("获取图片列表异常", err)
							}
							log.Println("还有更多图片，继续获取")
						}
					}

				}
			}
		}
	}
}

func get_images(cursor, cookie string) (*simplejson.Json, error) {
	url := "https://photo.baidu.com/youai/file/v1/list?clienttype=70&need_filter_hidden=0"
	if cursor != "" {
		url = url + "&cursor=" + cursor
	}

	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	request.Header.Add("Cookie", cookie)

	response, err := http_client.Do(request)
	if err != nil {
		return nil, err
	}

	body := response.Body
	bytes, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	json, err := simplejson.NewJson(bytes)
	return json, err
}

func download_iamge(cookie string, imageList []ImageItem) {
	bytes, _ := os.ReadFile(downloadFilename)
	var downloadList = make(map[string]int)
	if bytes != nil {
		//_ = json2.Unmarshal(bytes, &downloadList)
		split := strings.Split(string(bytes), ",")
		for _, md5 := range split {
			downloadList[md5] = 1
		}
	}

	file, err := os.OpenFile(downloadFilename, os.O_APPEND|os.O_WRONLY, os.ModePerm)
	if err != nil {
		log.Fatalln("打开文件失败", err)
	}

	intChan := make(chan int, concurrence_size)
	for i := 0; i < concurrence_size; i++ {
		intChan <- 1
	}

	var waitGroup sync.WaitGroup
	for _, item := range imageList {
		<-intChan
		waitGroup.Add(1)
		go doFetchFile(&waitGroup, intChan, file, downloadList, cookie, item)
	}

	waitGroup.Wait()
	log.Println("文件下载完成============")
}

func doFetchFile(waitGroup *sync.WaitGroup, chanInt chan int, file *os.File, downloadList map[string]int, cookie string, item ImageItem) {
	defer waitGroup.Done()
	defer func() { chanInt <- 1 }()

	md5 := item.Md5
	_, exist := downloadList[md5]
	fsid := item.Fsid

	if !exist {
		// 不存在，获取
		url := "https://photo.baidu.com/youai/file/v2/download?clienttype=70&fsid=" + fsid
		request, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}
		request.Header.Add("Cookie", cookie)

		response, err := http_client.Do(request)
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}
		body := response.Body
		bytes, err := io.ReadAll(body)
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}

		res, err := simplejson.NewJson(bytes)
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}
		code, err := res.Get("errno").Int()
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}
		if code != 0 {
			log.Println("下载部分图片失败，程序运行结束后请重试", res)
			return
		}

		// 成功
		downloadUrl, err := res.Get("dlink").String()
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}
		resp, err := http_client.Get(downloadUrl)
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}

		outputFilename := "images/" + item.Filename
		outputFile, err := os.Create(outputFilename)
		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}
		writer := bufio.NewWriterSize(outputFile, 1024)
		_, err = io.Copy(writer, resp.Body)
		_ = writer.Flush()
		defer func() { _ = outputFile.Close() }()

		if err != nil {
			log.Println("下载部分图片失败，程序运行结束后请重试", err)
			return
		}

		// 修改文件的创建时间，还原文件的最初时间
		if item.Ctime > 0 {
			file_ctime := time.Unix(item.Ctime, 0)
			_ = SetFileTime("images/"+item.Filename, file_ctime, file_ctime, file_ctime)
		}
		saveMd5(file, downloadList, md5)
	}
}

// 修改文件时间
func SetFileTime(path string, ctime, atime, mtime time.Time) (err error) {
	path, err = syscall.FullPath(path)
	if err != nil {
		return
	}
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return
	}
	handle, err := syscall.CreateFile(pathPtr, syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_WRITE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return
	}
	defer syscall.Close(handle)
	a := syscall.NsecToFiletime(syscall.TimespecToNsec(syscall.NsecToTimespec(atime.UnixNano())))
	c := syscall.NsecToFiletime(syscall.TimespecToNsec(syscall.NsecToTimespec(ctime.UnixNano())))
	m := syscall.NsecToFiletime(syscall.TimespecToNsec(syscall.NsecToTimespec(mtime.UnixNano())))
	return syscall.SetFileTime(handle, &c, &a, &m)
}

func checkFile(filename string) {
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		// 文件不存在，创建文件
		_, err := os.Create(filename)
		if err != nil {
			log.Fatalln("文件创建失败", err)
		}
	}
}

func saveMd5(file *os.File, downloadList map[string]int, md5 string) {
	saveMd5Lock.Lock()
	defer saveMd5Lock.Unlock()
	downloadList[md5] = 1
	_, _ = file.WriteString(md5 + ",")
}
