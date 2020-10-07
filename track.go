package mediadevices

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math/rand"
	"sync"

	"github.com/pion/mediadevices/pkg/codec"
	"github.com/pion/mediadevices/pkg/driver"
	"github.com/pion/mediadevices/pkg/io/audio"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/wave"
	"github.com/pion/webrtc/v2"
)

var (
	errInvalidDriverType = errors.New("invalid driver type")
)

type Source interface {
	ID() string
	Close() error
}

type VideoSource interface {
	video.Reader
	Source
}

type AudioSource interface {
	audio.Reader
	Source
}

type LocalTrackContext struct {
	PeerConnection *webrtc.PeerConnection
}

// Tracker is an interface that represent MediaStreamTrack
// Reference: https://w3c.github.io/mediacapture-main/#mediastreamtrack
type Track interface {
	Source
	// OnEnded registers a handler to receive an error from the media stream track.
	// If the error is already occured before registering, the handler will be
	// immediately called.
	OnEnded(func(error))
	Kind() MediaDeviceType
	Bind(LocalTrackContext) error
	Unbind(LocalTrackContext) error
}

type baseTrack struct {
	Source
	err            error
	onErrorHandler func(error)
	mu             sync.Mutex
	endOnce        sync.Once
	kind           MediaDeviceType
	selector       *CodecSelector
	activeTracks   map[*webrtc.PeerConnection]context.Context
}

type VideoTrack struct {
	*baseTrack
	*video.Broadcaster
}

type AudioTrack struct {
	*baseTrack
	*audio.Broadcaster
}

func newBaseTrack(source Source, kind MediaDeviceType, selector *CodecSelector) *baseTrack {
	return &baseTrack{Source: source, kind: kind, selector: selector}
}

// Kind returns track's kind
func (track *baseTrack) Kind() MediaDeviceType {
	return track.kind
}

// OnEnded sets an error handler. When a track has been created and started, if an
// error occurs, handler will get called with the error given to the parameter.
func (track *baseTrack) OnEnded(handler func(error)) {
	track.mu.Lock()
	track.onErrorHandler = handler
	err := track.err
	track.mu.Unlock()

	if err != nil && handler != nil {
		// Already errored.
		track.endOnce.Do(func() {
			handler(err)
		})
	}
}

// onError is a callback when an error occurs
func (track *baseTrack) onError(err error) {
	track.mu.Lock()
	track.err = err
	handler := track.onErrorHandler
	track.mu.Unlock()

	if handler != nil {
		track.endOnce.Do(func() {
			handler(err)
		})
	}
}

func (track *baseTrack) bind(ctx LocalTrackContext, encodedReader codec.ReadCloser, selectedCodec *codec.RTPCodec) error {
	track.mu.Lock()
	defer track.mu.Unlock()

	/*
		webrtcTrack, err := ctx.PeerConnection.NewTrack(selectedCodec.PayloadType, rand.Uint32(), track.ID(), selectedCodec.MimeType)
		if err != nil {
			return nil
		}
		go func() {
			var n int
			var err error
			buff := make([]byte, 1024)
			for {
				n, err = t.encoder.Read(buff)
				if err != nil {
					if e, ok := err.(*mio.InsufficientBufferError); ok {
						buff = make([]byte, 2*e.RequiredSize)
						continue
					}

					t.onError(err)
					return
				}

				if err := t.sample(buff[:n]); err != nil {
					t.onError(err)
					return
				}
			}
		}()
	*/

	return nil
}

func (track *baseTrack) unbind(ctx LocalTrackContext) error {
	track.mu.Lock()
	defer track.mu.Unlock()

	return nil
}

func NewVideoTrack(source VideoSource, selector *CodecSelector) Track {
	return newVideoTrackFromReader(source, source, selector)
}

func newVideoTrackFromReader(source Source, reader video.Reader, selector *CodecSelector) Track {
	base := newBaseTrack(source, VideoInput, selector)
	wrappedReader := video.ReaderFunc(func() (img image.Image, err error) {
		img, err = reader.Read()
		if err != nil {
			base.onError(err)
		}
		return img, err
	})

	// TODO: Allow users to configure broadcaster
	broadcaster := video.NewBroadcaster(wrappedReader, nil)

	return &VideoTrack{
		baseTrack:   base,
		Broadcaster: broadcaster,
	}
}

func NewAudioTrack(source AudioSource, selector *CodecSelector) Track {
	return newAudioTrackFromReader(source, source, selector)
}

func newAudioTrackFromReader(source Source, reader audio.Reader, selector *CodecSelector) Track {
	base := newBaseTrack(source, AudioInput, selector)
	wrappedReader := audio.ReaderFunc(func() (chunk wave.Audio, err error) {
		chunk, err = reader.Read()
		if err != nil {
			base.onError(err)
		}
		return chunk, err
	})

	// TODO: Allow users to configure broadcaster
	broadcaster := audio.NewBroadcaster(wrappedReader, nil)

	return &AudioTrack{
		baseTrack:   base,
		Broadcaster: broadcaster,
	}
}

func newTrackFromDriver(d driver.Driver, constraints MediaTrackConstraints, selector *CodecSelector) (Track, error) {
	if err := d.Open(); err != nil {
		return nil, err
	}

	switch recorder := d.(type) {
	case driver.VideoRecorder:
		return newVideoTrackFromDriver(d, recorder, constraints, selector)
	case driver.AudioRecorder:
		return newAudioTrackFromDriver(d, recorder, constraints, selector)
	default:
		panic(errInvalidDriverType)
	}
}

// newVideoTrackFromDriver is an internal video track creation from driver
func newVideoTrackFromDriver(d driver.Driver, recorder driver.VideoRecorder, constraints MediaTrackConstraints, selector *CodecSelector) (Track, error) {
	reader, err := recorder.VideoRecord(constraints.selectedMedia)
	if err != nil {
		return nil, err
	}

	return newVideoTrackFromReader(d, reader, selector), nil
}

// newAudioTrackFromDriver is an internal audio track creation from driver
func newAudioTrackFromDriver(d driver.Driver, recorder driver.AudioRecorder, constraints MediaTrackConstraints, selector *CodecSelector) (Track, error) {
	reader, err := recorder.AudioRecord(constraints.selectedMedia)
	if err != nil {
		return nil, err
	}

	return newAudioTrackFromReader(d, reader, selector), nil
}

func (track *VideoTrack) Bind(ctx LocalTrackContext) error {
	wantCodecs := ctx.PeerConnection.GetRegisteredRTPCodecs(webrtc.RTPCodecTypeVideo)
	encodedReader, err := track.selector.selectVideoCodec(wantCodecs, track)
	if err != nil {
		return err
	}

	return nil
}
