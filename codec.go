package mediadevices

import (
	"errors"
	"fmt"
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

type CodecSelector struct {
	videoEncoders []codec.VideoEncoderBuilder
	audioEncoders []codec.AudioEncoderBuilder
}

type CodecSelectorOption func(*CodecSelector)

func WithVideoEncoders(encoders ...codec.VideoEncoderBuilder) CodecSelectorOption {
	return func(t *CodecSelector) {
		t.videoEncoders = encoders
	}
}

func WithAudioEncoders(encoders ...codec.AudioEncoderBuilder) CodecSelectorOption {
	return func(t *CodecSelector) {
		t.audioEncoders = encoders
	}
}

func NewCodecSelector(opts ...CodecSelectorOption) *CodecSelector {
	var track CodecSelector

	for _, opt := range opts {
		opt(&track)
	}

	return &track
}

func (selector *CodecSelector) Populate(setting *webrtc.MediaEngine) {
	for _, encoder := range selector.videoEncoders {
		setting.RegisterCodec(encoder.RTPCodec().RTPCodec)
	}

	for _, encoder := range selector.audioEncoders {
		setting.RegisterCodec(encoder.RTPCodec().RTPCodec)
	}
}

func (selector *CodecSelector) selectVideoCodec(wantCodecs []*webrtc.RTPCodec, track *VideoTrack) (codec.ReadCloser, error) {
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

outer:
	for _, wantCodec := range wantCodecs {
		name := wantCodec.Name
		for _, encoder := range selector.videoEncoders {
			if encoder.RTPCodec().Name == name {
				encodedReader, err = encoder.BuildVideoEncoder(reader, currentProp)
				if err == nil {
					selectedEncoder = encoder
					break outer
				}
			}

			errReasons = append(errReasons, fmt.Sprintf("%s: %s", encoder.RTPCodec().Name, err))
		}
	}

	if selectedEncoder == nil {
		return nil, errors.New(strings.Join(errReasons, "\n\n"))
	}

	return encodedReader, nil
}
