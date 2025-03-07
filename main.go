package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"io/ioutil"
	"log"
	"math"
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
	Jobs = make([]*Item, 0)
	go Dequeue()
	go GetQueue()
	r := gin.Default()
	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

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
	update(i.Name)
	return nil

}

func update(name string) {
	url := "https://putio.bramsoft.com/update"
	method := "POST"

	payload := strings.NewReader(fmt.Sprintf("{\"name\": \"%s\"}", name))

	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close()

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(body))
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
	if len(Jobs) == 0 || len(CurrentJobs) > 3 {
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
		CurrentJobQueue = append(CurrentJobQueue, job.Name+" - "+job.CompletedPercent+"% completed")
	}

	resp := gin.H{
		"totalJobs": totalJobs,
		"queue":     JobQueue,
		"working":   CurrentJobQueue,
		"message":   fmt.Sprintf("%d", totalJobs),
	}
	c.JSON(http.StatusOK, resp)
}

func GetQueue() {

	fmt.Println("getting queue")

	url := "putio.bramsoft.com/queue"
	method := "POST"
	client := &http.Client{}
	req, err := http.NewRequest(method, url, nil)

	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}

	var items []*Item

	if err := json.Unmarshal(body, &items); err != nil {
		fmt.Println(err)
	}
	Jobs = append(Jobs, items...)
	time.Sleep(time.Minute * 5)
	GetQueue()
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
			if math.Mod(percent, 5) == 0 {
				UpdateQueue(i)
			}
			i.CompletedPercent = fmt.Sprintf("%.0f", percent)
		}

		if stop {
			break
		}

		time.Sleep(time.Second)
	}
}

func UpdateQueue(item *Item) {
	url := "putio.bramsoft.com/updateQueue"
	method := "POST"

	arr, _ := json.Marshal(item)
	payload := bytes.NewReader(arr)
	client := &http.Client{}
	req, err := http.NewRequest(method, url, payload)

	if err != nil {
		fmt.Println(err)
		return
	}
	req.Header.Add("Content-Type", "application/json")

	res, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(body))
}

type Item struct {
	URL              string      `json:"url"`
	Type             ContentType `json:"type"`
	Name             string      `json:"name"`
	FileId           int64       `json:"file_id"`
	Started          bool        `json:"started"`
	CompletedPercent string      `json:"completed_percent"`
	Completed        bool        `json:"completed"`
}

type ContentType string

const (
	Movies ContentType = "movie"
	Anime  ContentType = "anime"
	TVShow ContentType = "tvshow"
)
