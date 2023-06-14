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
	"strings"
	"time"
)

var Jobs []*Item
var MoviesPath string
var TVShowPath string
var PORT string

// var Interval int64
var CurrentJobs map[string]*Item

func main() {
	CurrentJobs = make(map[string]*Item)
	MoviesPath = os.Getenv("MOVIES_PATH")
	TVShowPath = os.Getenv("TVSHOW_PATH")
	PORT = os.Getenv("PORT")
	//interval = os.Getenv("INTERVAL")
	//inter, err := strconv.Atoi(interval)
	//if err != nil {
	//	fmt.Println("Error converting interval to int")
	//	os.Exit(80)
	//}
	//
	//Interval = int64(inter)

	Jobs = make([]*Item, 0)
	go Dequeue()
	r := gin.Default()
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	r.POST("/dl", HandleDownload)
	r.GET("/queue", Queue)
	if err := r.Run(":" + PORT); err != nil {
		fmt.Println(err)
	}

}

func (i *Item) StartDownload() error {
	defer func() {
		delete(CurrentJobs, i.URL)
	}()
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
		return err
	}

	defer headResp.Body.Close()

	size, err := strconv.Atoi(headResp.Header.Get("Content-Length"))

	if err != nil {
		return err
	}

	done := make(chan int64)

	go i.UpdateDownloadPercent(done, path.String(), int64(size))

	resp, err := http.Get(i.URL)

	if err != nil {
		return err
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

func HandleDownload(c *gin.Context) {
	var json Item
	if err := c.BindJSON(&json); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "unable to bind JSON",
		})
		fmt.Println(err)
		return
	}

	strings.ReplaceAll(json.Name, " ", ".")

	Jobs = append(Jobs, &json)
	c.JSON(http.StatusOK, gin.H{"message": fmt.Sprintf("Added to Jobs. Current Queue Count %d", len(Jobs))})
}

func Dequeue() {
	if len(Jobs) == 0 && len(CurrentJobs) <= 3 {
		time.Sleep(time.Second * 30)
		go Dequeue()
		return
	}
	fmt.Println("Total number of Jobs queued: ", len(Jobs))
	job := Jobs[0]
	CurrentJobs[job.URL] = job
	go func() {
		if err := job.StartDownload(); err != nil {
			fmt.Println("error with job for " + job.Name + " going to retry again...")
			fmt.Println("error was: ", err)
			Jobs = append(Jobs, job)
		}
	}()

	Jobs = Jobs[1:]
	time.Sleep(time.Second * 30)
	go Dequeue()
}

func Queue(c *gin.Context) {

	totalJobs := len(Jobs) + len(CurrentJobs)

	if totalJobs == 0 {
		c.JSON(http.StatusOK, gin.H{
			"message": "no jobs in queue",
		})
		return
	}

	var JobQueue []string
	for _, job := range Jobs {
		JobQueue = append(JobQueue, job.Name)
	}
	var CurrentJobQueue []string
	for _, job := range CurrentJobs {
		CurrentJobQueue = append(CurrentJobQueue, job.Name+" - "+job.CompletedPercent+" completed")
	}

	resp := gin.H{
		"totalJobs": totalJobs,
		"queue":     JobQueue,
		"working":   CurrentJobQueue,
		"message":   fmt.Sprintf("%d", totalJobs),
	}
	c.JSON(http.StatusOK, resp)
}

func (i *Item) UpdateDownloadPercent(done chan int64, path string, total int64) {

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
			i.CompletedPercent = fmt.Sprintf("%.0f", percent)
		}

		if stop {
			break
		}

		time.Sleep(time.Second)
	}
}

type Item struct {
	URL              string      `json:"url"`
	Type             ContentType `json:"type"`
	Name             string      `json:"name"`
	Started          bool        `json:"started"`
	CompletedPercent string      `json:"completed"`
}

type ContentType string

const (
	Movies ContentType = "movie"
	Anime  ContentType = "anime"
	TVShow ContentType = "tvshow"
)
