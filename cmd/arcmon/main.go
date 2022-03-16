package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	ArcDpsURL           = "https://www.deltaconnected.com/arcdps/x64/"
	ArcDPSCheckSumURL   = ArcDpsURL + "d3d9.dll.md5sum"
	ArcDPSDLLURL        = ArcDpsURL + "d3d9.dll"
	DefaultTickDuration = 10 * time.Minute
)

type ArcDPSVersion struct {
	Timestamp    time.Time `yaml:"timestamp"`
	CheckSum     string    `yaml:"check_sum"`
	sync.RWMutex `yaml:"-"`
}

func main() {
	if os.Getenv("DISCORD_WEBHOOK") == "" {
		logrus.Fatalf("missing DISCORD_WEBHOOK env variable")
	}

	f, err := os.OpenFile(filepath.Join(".", "arcdps.yml"), os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		if !strings.Contains(err.Error(), "no such") {
			logrus.Fatalf("err opening tracking file: %v\n", err)
		}
	}

	logrus.Infof("using: %s", f.Name())

	arcdps := &ArcDPSVersion{}
	if err := yaml.NewDecoder(f).Decode(&arcdps); err != nil && err != io.EOF {
		logrus.Fatalf("unable to decode arcdps.yml: %v", err)
	}

	s := NewServer(arcdps)
	ctx, cncl := context.WithCancel(context.Background())
	go s.Tick(ctx)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGKILL, os.Interrupt)
	<-sig
	cncl()
	logrus.Infof("shutting down")
	f.Seek(0, 0) //rewind file descriptor
	if err := yaml.NewEncoder(f).Encode(arcdps); err != nil {
		logrus.Fatalf("unable to save file: (%v)", err)
	}
	f.Close()
}

type Server struct {
	http       *http.Client
	webhookURL string
	arcdps     *ArcDPSVersion
}

func NewServer(arcdps *ArcDPSVersion) *Server {
	return &Server{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
			Timeout: 5 * time.Second,
		},
		webhookURL: os.Getenv("DISCORD_WEBHOOK"),
		arcdps:     arcdps,
	}
}

func (s *Server) Tick(ctx context.Context) {
	ticker := time.NewTicker(DefaultTickDuration)
	logrus.Infof("Starting Check Ticker")
	for {
		select {
		case <-ticker.C:
			check, err := s.GetChecksum(ctx)
			if err != nil {
				logrus.Errorf("Failed getting checksum: (%v)", err)
				continue
			}
			version, err := s.GetVersion(ctx)
			if err != nil {
				logrus.Errorf("Failed getting version: (%v)", err)
				continue
			}
			if s.arcdps.CheckSum == "" {
				logrus.Infof("Setting initial version")
				s.arcdps.CheckSum = check
				s.arcdps.Timestamp = version
				continue
			}
			if s.arcdps.CheckSum != check {
				// new version
				if err := s.SendWebHook(ctx,
					fmt.Sprintf("`%s`", check),
					fmt.Sprintf("`%s`", version.String()),
				); err != nil {
					logrus.Errorf("unable to send webhook: (%v)\n", err)
				}
				s.arcdps.CheckSum = check
				s.arcdps.Timestamp = version
			}
		case <-ctx.Done():
			ticker.Stop()
			return
		}
	}
}

func (s *Server) GetChecksum(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", ArcDPSCheckSumURL, nil)
	if err != nil {
		return "", err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode > 299 {
		return "", fmt.Errorf("bad response from delta: (%s)", string(body))
	}

	checkSumSplit := strings.Split(string(body), " ")
	if len(checkSumSplit) < 2 {
		return "", fmt.Errorf("incorrect size of checksum split")
	}
	return checkSumSplit[0], nil

}

func (s *Server) GetVersion(ctx context.Context) (time.Time, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", ArcDPSDLLURL, nil)
	if err != nil {
		return time.Time{}, err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return time.Time{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return time.Time{}, err
		}
		return time.Time{}, fmt.Errorf("bad response from delta: (%s)", string(body))
	}
	lastModified, err := time.Parse(time.RFC1123, resp.Header.Get("Last-Modified"))
	if err != nil {
		return time.Time{}, fmt.Errorf("unable to parse time: (%v)", err)
	}

	return lastModified, nil
}

func (s *Server) SendWebHook(ctx context.Context, checksum, time string) error {
	payload := bytes.NewBufferString(fmt.Sprintf(PayloadJSON, checksum, time, DefaultTickDuration.String()))
	req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, payload)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		return fmt.Errorf("bad response from Discord: %d (%s)", resp.StatusCode, string(body))
	}
	return nil
}

var (
	PayloadJSON = `
{
  "embeds": [
    {
      "title": "ArcDPS has updated!",
      "color": 12124160,
      "fields": [
        {
          "name": "Checksum",
          "value": "%s",
          "inline": true
        },
        {
          "name": "Timestamp Version",
          "value": "%s",
          "inline": true
        },
        {
          "name": "Direct Download Link",
          "value": "https://www.deltaconnected.com/arcdps/x64/d3d9.dll"
        }
      ],
      "author": {
        "name": "ArcDPS Monitor",
        "icon_url": "https://wiki.guildwars2.com/images/0/03/Specter_icon_(highres).png"
      },
      "footer": {
        "text": "This bot checks every %s"
      }
    }
  ]
}`
)
