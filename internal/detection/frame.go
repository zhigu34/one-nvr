package detection

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/zhigu34/one-nvr/internal/auth"
	"github.com/zhigu34/one-nvr/internal/fault"
	"github.com/zhigu34/one-nvr/internal/id"
)

// DetectWidth/DetectHeight are the pixel size the detector renders every
// camera at. Frigate scales each input to exactly this size (stretching, not
// letterboxing), so a normalized zone keeps meaning the same part of the view
// whatever the camera's own resolution is — but the frame's aspect ratio is
// what the operator actually draws against, which is why the canvas shows a
// still at this exact size.
const (
	DetectWidth  = 640
	DetectHeight = 360
)

// Frame errors. A frame that is not there yet is a state, not a fault.
var (
	ErrFrameUnavailable = fault.New(503, "detection_frame_unavailable", "检测画面暂时不可读")
	ErrFrameNotReady    = fault.New(409, "detection_frame_not_ready", "检测画面尚未就绪，请先发布并等待取流")
)

// Frame returns one still of the frame the detector analyzes, at the size the
// detector sees it. Drawing on anything else would invite zones that describe
// pixels the detector never looks at: a camera snapshot is a different stream
// (main vs sub), a different scale, and after a lens or profile change a
// different field of view.
func (s *Service) Frame(ctx context.Context, p auth.Principal, channelID id.ID) ([]byte, string, error) {
	if s.Auth == nil || s.Frigate == "" {
		return nil, "", auth.ErrNotFound
	}
	if err := s.Auth.RequireChannel(ctx, p, channelID, auth.Configure); err != nil {
		return nil, "", err
	}
	// Only a channel the detector actually watches has a frame to show. An
	// enabled-but-unpublished channel has no camera in the detector's config.
	var channelNo int
	err := s.DB.Pool.QueryRow(ctx, `SELECT c.channel_no FROM channels c JOIN detection_configs d ON d.channel_id=c.id AND d.enabled WHERE c.id=$1`, channelID).Scan(&channelNo)
	if err != nil {
		return nil, "", ErrFrameNotReady
	}
	return s.cameraFrame(ctx, CameraName(channelNo))
}

// cameraFrame fetches one still from the detector's HTTP API. The detector
// serves the frame its object detector is working on, which is precisely the
// image the zone coordinates are relative to.
func (s *Service) cameraFrame(ctx context.Context, camera string) ([]byte, string, error) {
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target := s.Frigate + "/api/" + camera + "/latest.jpg?height=" + strconv.Itoa(DetectHeight)
	request, err := http.NewRequestWithContext(bounded, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", ErrFrameUnavailable
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", ErrFrameUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusBadRequest {
		// Nothing to show yet: the camera is in the config but has not produced
		// a frame, or the detector answered with its own error image.
		return nil, "", ErrFrameNotReady
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", ErrFrameUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, "", ErrFrameUnavailable
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	return data, contentType, nil
}
