package arcmon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	Timestamp time.Time `yaml:"timestamp"`
	CheckSum  string    `yaml:"check_sum"`
}

func main() {
	f, err := os.Open(filepath.Dir(os.TempDir() + "/arcdps.yaml"))
	if err != nil {
		if err != os.ErrNotExist {
			logrus.Fatalf("err opening temp file: %v\n", err)
		}
		f, err = os.CreateTemp("", "/arcdps.yml")
		if err != nil {
			logrus.Fatalf("temp file not exists and unable to create new one: %v\n", err)
		}
	}

	arcdps := &ArcDPSVersion{}
	if err := yaml.NewDecoder(f).Decode(&arcdps); err != nil {
		logrus.Fatalf("unable to decode arcdps.yml: %v", err)
	}

	s := NewServer()
	s.Tick()

}

type Server struct {
	http       *http.Client
	webhookURL string
}

func NewServer() *Server {
	return &Server{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			},
			Timeout: 5 * time.Second,
		},
		webhookURL: os.Getenv("DISCORD_WEBHOOK"),
	}
}

func (s *Server) Tick() {

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
	payload := bytes.NewBufferString(fmt.Sprintf(PayloadJSON, checksum, time))
	req, err := http.NewRequestWithContext(ctx, "POST", s.webhookURL, payload)
	if err != nil {
		return err
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode > 299 {
		return fmt.Errorf("bad response from Discord: %d", resp.StatusCode)
	}
	return nil
}

var (
	PayloadJSON = `
{
  "content": null,
  "embeds": [
    {
      "title": "ArcDPS has updated!",
      "color": 12124160,
      "fields": [
        {
          "name": "CheckSum",
          "value": "%s"
        },
        {
          "name": "Timestamp Version",
          "value": "%s",
          "inline": true
        },
        {
          "name": "Direct Download Link",
          "value": "https://www.deltaconnected.com/arcdps/x64/d3d9.dll",
          "inline": true
        }
      ],
      "footer": {
        "text": "ArcDPS Monitor"
      }
    }
  ]
}`
)
