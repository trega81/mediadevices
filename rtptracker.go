package mediadevices

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/pion/mediadevices/pkg/codec"
	mio "github.com/pion/mediadevices/pkg/io"
	"github.com/pion/mediadevices/pkg/io/video"
	"github.com/pion/mediadevices/pkg/prop"
	"github.com/pion/webrtc/v2"
)

var (
	errInvalidTrackType = errors.New("invalid track type")
)

type RTPTrack struct {
	videoEncoders []codec.VideoEncoderBuilder
	audioEncoders []codec.AudioEncoderBuilder
}

type RTPTrackOption func(*RTPTrack)

func WithVideoEncoders(encoders ...codec.VideoEncoderBuilder) RTPTrackOption {
	return func(t *RTPTrack) {
		t.videoEncoders = encoders
	}
}

func WithAudioEncoders(encoders ...codec.AudioEncoderBuilder) RTPTrackOption {
	return func(t *RTPTrack) {
		t.audioEncoders = encoders
	}
}

func NewRTPTrack(opts ...RTPTrackOption) *RTPTrack {
	var track RTPTrack

	for _, opt := range opts {
		opt(&track)
	}

	return &track
}

func (rtpTrack *RTPTrack) Populate(setting *webrtc.MediaEngine) {
	for _, encoder := range rtpTrack.videoEncoders {
		setting.RegisterCodec(encoder.RTPCodec().RTPCodec)
	}

	for _, encoder := range rtpTrack.audioEncoders {
		setting.RegisterCodec(encoder.RTPCodec().RTPCodec)
	}
}

func (rtpTrack *RTPTrack) Wrap(pc *webrtc.PeerConnection, track Track) (*webrtc.Track, error) {
	switch track := track.(type) {
	case *VideoTrack:
		return rtpTrack.wrapVideoTrack(pc, track)
	default:
		panic(errInvalidTrackType)
	}
}

func (rtpTrack *RTPTrack) wrapVideoTrack(pc *webrtc.PeerConnection, track *VideoTrack) (*webrtc.Track, error) {
	var currentProp prop.Media
	var err error
	metaReader := track.NewReader(false)
	metaReader = video.DetectChanges(time.Second, func(p prop.Media) { currentProp = p })(metaReader)
	_, err = metaReader.Read()
	if err != nil {
		return nil, err
	}

	// TODO: Should also detect changes in the main media pipeline and adjust the encoder accordingly
	reader := track.NewReader(false)
	var selectedEncoder codec.VideoEncoderBuilder
	var encodedReader codec.ReadCloser
	var errReasons []string
	for _, encoder := range rtpTrack.videoEncoders {
		encodedReader, err = encoder.BuildVideoEncoder(reader, currentProp)
		if err == nil {
			selectedEncoder = encoder
			break
		}

		errReasons = append(errReasons, fmt.Sprintf("%s: %s", encoder.RTPCodec().Name, err))
	}

	if selectedEncoder == nil {
		return nil, errors.New(strings.Join(errReasons, "\n\n"))
	}

	rtpCodec := selectedEncoder.RTPCodec()
	webrtcTrack, err := pc.NewTrack(rtpCodec.PayloadType, rand.Uint32(), track.ID(), rtpCodec.Type.String())
	if err != nil {
		encodedReader.Close()
		return nil, err
	}

	sample := newVideoSampler(webrtcTrack)

	go func() {
		var n int
		var err error
		buff := make([]byte, 1024)
		for {
			n, err = encodedReader.Read(buff)
			if err != nil {
				if e, ok := err.(*mio.InsufficientBufferError); ok {
					buff = make([]byte, 2*e.RequiredSize)
					continue
				}

				track.onError(err)
				return
			}

			if err := sample(buff[:n]); err != nil {
				track.onError(err)
				return
			}
		}
	}()

	return webrtcTrack, nil
}
