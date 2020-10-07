package mediadevices

import (
	"fmt"
	"math"
	"strings"

	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v2"
)

var errNotFound = fmt.Errorf("failed to find the best driver that fits the constraints")

// MediaDevices is an interface that's defined on https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices
type MediaDevices interface {
	GetDisplayMedia(constraints MediaStreamConstraints) (MediaStream, error)
	GetUserMedia(constraints MediaStreamConstraints) (MediaStream, error)
	EnumerateDevices() []MediaDeviceInfo
}

// NewMediaDevices creates MediaDevices interface that provides access to connected media input devices
// like cameras and microphones, as well as screen sharing.
// In essence, it lets you obtain access to any hardware source of media data.
func NewMediaDevices(pc *webrtc.PeerConnection, opts ...MediaDevicesOption) MediaDevices {
	codecs := make(map[webrtc.RTPCodecType][]*webrtc.RTPCodec)
	for _, kind := range []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeAudio,
		webrtc.RTPCodecTypeVideo,
	} {
		codecs[kind] = pc.GetRegisteredRTPCodecs(kind)
	}
	return NewMediaDevicesFromCodecs(codecs, opts...)
}

// NewMediaDevicesFromCodecs creates MediaDevices interface from lists of the available codecs
// that provides access to connected media input devices like cameras and microphones,
// as well as screen sharing.
// In essence, it lets you obtain access to any hardware source of media data.
func NewMediaDevicesFromCodecs(codecs map[webrtc.RTPCodecType][]*webrtc.RTPCodec, opts ...MediaDevicesOption) MediaDevices {
	mdo := MediaDevicesOptions{
		codecs: codecs,
	}
	for _, o := range opts {
		o(&mdo)
	}
	return &mediaDevices{
		MediaDevicesOptions: mdo,
	}
}

type mediaDevices struct {
	MediaDevicesOptions
}

// MediaDevicesOptions stores parameters used by MediaDevices.
type MediaDevicesOptions struct {
	codecs map[webrtc.RTPCodecType][]*webrtc.RTPCodec
}

// MediaDevicesOption is a type of MediaDevices functional option.
type MediaDevicesOption func(*MediaDevicesOptions)

// GetDisplayMedia prompts the user to select and grant permission to capture the contents
// of a display or portion thereof (such as a window) as a MediaStream.
// Reference: https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getDisplayMedia
func (m *mediaDevices) GetDisplayMedia(constraints MediaStreamConstraints) (MediaStream, error) {
	trackers := make([]Track, 0)

	cleanTrackers := func() {
		for _, t := range trackers {
			t.Close()
		}
	}

	var videoConstraints MediaTrackConstraints
	if constraints.Video != nil {
		constraints.Video(&videoConstraints)
	}

	if videoConstraints.Enabled {
		tracker, err := m.selectScreen(videoConstraints, constraints.Codec)
		if err != nil {
			cleanTrackers()
			return nil, err
		}

		trackers = append(trackers, tracker)
	}

	s, err := NewMediaStream(trackers...)
	if err != nil {
		cleanTrackers()
		return nil, err
	}

	return s, nil
}

// GetUserMedia prompts the user for permission to use a media input which produces a MediaStream
// with tracks containing the requested types of media.
// Reference: https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getUserMedia
func (m *mediaDevices) GetUserMedia(constraints MediaStreamConstraints) (MediaStream, error) {
	// TODO: It should return media stream based on constraints
	trackers := make([]Track, 0)

	cleanTrackers := func() {
		for _, t := range trackers {
			t.Close()
		}
	}

	var videoConstraints, audioConstraints MediaTrackConstraints
	if constraints.Video != nil {
		constraints.Video(&videoConstraints)
	}

	if constraints.Audio != nil {
		constraints.Audio(&audioConstraints)
	}

	if videoConstraints.Enabled {
		tracker, err := m.selectVideo(videoConstraints, constraints.Codec)
		if err != nil {
			cleanTrackers()
			return nil, err
		}

		trackers = append(trackers, tracker)
	}

	if audioConstraints.Enabled {
		tracker, err := m.selectAudio(audioConstraints, constraints.Codec)
		if err != nil {
			cleanTrackers()
			return nil, err
		}

		trackers = append(trackers, tracker)
	}

	s, err := NewMediaStream(trackers...)
	if err != nil {
		cleanTrackers()
		return nil, err
	}

	return s, nil
}

func queryDriverProperties(filter driver.FilterFn) map[driver.Driver][]prop.Media {
	var needToClose []driver.Driver
	drivers := driver.GetManager().Query(filter)
	m := make(map[driver.Driver][]prop.Media)

	for _, d := range drivers {
		if d.Status() == driver.StateClosed {
			err := d.Open()
			if err != nil {
				// Skip this driver if we failed to open because we can't get the properties
				continue
			}
			needToClose = append(needToClose, d)
		}

		m[d] = d.Properties()
	}

	for _, d := range needToClose {
		// Since it was closed, we should close it to avoid a leak
		d.Close()
	}

	return m
}

// select implements SelectSettings algorithm.
// Reference: https://w3c.github.io/mediacapture-main/#dfn-selectsettings
func selectBestDriver(filter driver.FilterFn, constraints MediaTrackConstraints) (driver.Driver, MediaTrackConstraints, error) {
	var bestDriver driver.Driver
	var bestProp prop.Media
	minFitnessDist := math.Inf(1)

	driverProperties := queryDriverProperties(filter)
	for d, props := range driverProperties {
		priority := float64(d.Info().Priority)
		for _, p := range props {
			fitnessDist, ok := constraints.MediaConstraints.FitnessDistance(p)
			if !ok {
				continue
			}
			fitnessDist -= priority
			if fitnessDist < minFitnessDist {
				minFitnessDist = fitnessDist
				bestDriver = d
				bestProp = p
			}
		}
	}

	if bestDriver == nil {
		var foundProperties []string
		for _, props := range driverProperties {
			for _, p := range props {
				foundProperties = append(foundProperties, fmt.Sprint(&p))
			}
		}

		err := fmt.Errorf(`%w:
============ Found Properties ============

%s

=============== Constraints ==============

%s
`, errNotFound, strings.Join(foundProperties, "\n\n"), &constraints)
		return nil, MediaTrackConstraints{}, err
	}

	constraints.selectedMedia = prop.Media{}
	constraints.selectedMedia.MergeConstraints(constraints.MediaConstraints)
	constraints.selectedMedia.Merge(bestProp)
	return bestDriver, constraints, nil
}

func (m *mediaDevices) selectAudio(constraints MediaTrackConstraints, selector *CodecSelector) (Track, error) {
	typeFilter := driver.FilterAudioRecorder()

	d, c, err := selectBestDriver(typeFilter, constraints)
	if err != nil {
		return nil, err
	}

	return newTrackFromDriver(d, c, selector)
}
func (m *mediaDevices) selectVideo(constraints MediaTrackConstraints, selector *CodecSelector) (Track, error) {
	typeFilter := driver.FilterVideoRecorder()
	notScreenFilter := driver.FilterNot(driver.FilterDeviceType(driver.Screen))
	filter := driver.FilterAnd(typeFilter, notScreenFilter)

	d, c, err := selectBestDriver(filter, constraints)
	if err != nil {
		return nil, err
	}

	return newTrackFromDriver(d, c, selector)
}

func (m *mediaDevices) selectScreen(constraints MediaTrackConstraints, selector *CodecSelector) (Track, error) {
	typeFilter := driver.FilterVideoRecorder()
	screenFilter := driver.FilterDeviceType(driver.Screen)
	filter := driver.FilterAnd(typeFilter, screenFilter)

	d, c, err := selectBestDriver(filter, constraints)
	if err != nil {
		return nil, err
	}

	return newTrackFromDriver(d, c, selector)
}

func (m *mediaDevices) EnumerateDevices() []MediaDeviceInfo {
	drivers := driver.GetManager().Query(
		driver.FilterFn(func(driver.Driver) bool { return true }))
	info := make([]MediaDeviceInfo, 0, len(drivers))
	for _, d := range drivers {
		var kind MediaDeviceType
		switch {
		case driver.FilterVideoRecorder()(d):
			kind = VideoInput
		case driver.FilterAudioRecorder()(d):
			kind = AudioInput
		default:
			continue
		}
		driverInfo := d.Info()
		info = append(info, MediaDeviceInfo{
			DeviceID:   d.ID(),
			Kind:       kind,
			Label:      driverInfo.Label,
			DeviceType: driverInfo.DeviceType,
		})
	}
	return info
}
