package main

import (
	"bytes"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

var Jobs []*Item
var MoviesPath string
var TVShowPath string
var PORT string
var interval string
var Interval int64

func main() {

	MoviesPath = os.Getenv("MOVIES_PATH")
	TVShowPath = os.Getenv("TVSHOW_PATH")
	PORT = os.Getenv("PORT")
	interval = os.Getenv("INTERVAL")
	inter, err := strconv.Atoi(interval)
	if err != nil {
		fmt.Println("Error converting interval to int")
		os.Exit(80)
	}

	Interval = int64(inter)

	Jobs = make([]*Item, 0)
	go Dequeue()
	r := gin.Default()
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	r.POST("/dl", HandleDownload)

	if err := r.Run(":" + PORT); err != nil {
		fmt.Println(err)
	}

}

func (i *Item) StartDownload() error {
	destination := MoviesPath
	if i.Type == TVShow {
		destination = TVShowPath
	}

	var path bytes.Buffer
	path.WriteString(destination + "/" + i.Name)
	start := time.Now()

	out, err := os.Create(path.String())

	if err != nil {
		fmt.Println(path.String())
		return err
	}

	defer out.Close()

	headResp, err := http.Head(i.URL)

	if err != nil {
		panic(err)
	}

	defer headResp.Body.Close()

	size, err := strconv.Atoi(headResp.Header.Get("Content-Length"))

	if err != nil {
		return err
	}

	done := make(chan int64)

	go PrintDownloadPercent(done, path.String(), int64(size))

	resp, err := http.Get(i.URL)

	if err != nil {
		panic(err)
	}

	defer resp.Body.Close()

	n, err := io.Copy(out, resp.Body)

	if err != nil {
		return err
	}

	done <- n

	elapsed := time.Since(start)
	log.Printf("Download completed in %s for %s", elapsed, i.Name)

	return nil

}

func (i *Item) MoveFile() error {
	source := i.Name

	src, err := os.Open(source)
	if err != nil {
		return err
	}

	destination := MoviesPath
	if i.Type == TVShow {
		destination = TVShowPath
	}

	destination = destination + "/" + i.Name

	dst, err := os.Create(destination)
	if err != nil {
		src.Close()
		return err
	}
	_, err = io.Copy(dst, src)
	src.Close()
	dst.Close()
	if err != nil {
		return err
	}
	fi, err := os.Stat(source)
	if err != nil {
		os.Remove(destination)
		return err
	}
	err = os.Chmod(destination, fi.Mode())
	if err != nil {
		os.Remove(destination)
		return err
	}
	os.Remove(source)
	return nil
}

func HandleDownload(c *gin.Context) {
	var json Item
	if err := c.BindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "unable to bind JSON",
		})
		fmt.Println(err)
		return
	}
	Jobs = append(Jobs, &json)
}

func Dequeue() {
	if len(Jobs) == 0 {
		fmt.Println("No Jobs.. waiting "+interval+" minutes... current time ", time.Now())
		time.Sleep(time.Minute * 5)
		go Dequeue()
		return
	}

	fmt.Println("Total number of Jobs queued: ", len(Jobs))

	job := Jobs[0]
	go func() {
		if err := job.StartDownload(); err != nil {
			fmt.Println("error with job for " + job.Name + " going to retry again...")
			fmt.Println("error was: ", err)
			Jobs = append(Jobs, job)
		}
	}()

	Jobs = Jobs[1:]
	time.Sleep(time.Minute * 5)
	go Dequeue()
}

func PrintDownloadPercent(done chan int64, path string, total int64) {

	var stop bool = false

	for {
		select {
		case <-done:
			stop = true
		default:

			file, err := os.Open(path)
			if err != nil {
				log.Fatal(err)
			}

			fi, err := file.Stat()
			if err != nil {
				log.Fatal(err)
			}

			size := fi.Size()

			if size == 0 {
				size = 1
			}

			var percent = float64(size) / float64(total) * 100

			fmt.Printf("%.0f", percent)
			fmt.Println("% completed for " + path)
		}

		if stop {
			break
		}

		time.Sleep(time.Second * 60)
	}
}

type Item struct {
	URL  string      `json:"url"`
	Type ContentType `json:"type"`
	Name string      `json:"name"`
}

type ContentType string

const (
	Movies ContentType = "movie"
	Anime  ContentType = "anime"
	TVShow ContentType = "tvshow"
)
