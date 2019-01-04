package sequencer

import (
	"context"
	"encoding/json"
	"sync"

	toml "github.com/BurntSushi/toml"

	"github.com/geliar/manopus/pkg/input"
	"github.com/geliar/manopus/pkg/output"
	"github.com/geliar/manopus/pkg/payload"
	"github.com/geliar/manopus/pkg/processor"
)

type Sequencer struct {
	//Env variables which represent env part of context data
	Env map[string]interface{} `yaml:"env"`
	//DefaultInputs the list of inputs which should be matched if inputs list in step is empty
	DefaultInputs []string `yaml:"default_inputs"`
	//DefaultOutputs the list of outputs which should be used to send responses
	//if outputs list in step is empty
	DefaultOutputs []output.OutputConfig `yaml:"default_outputs"`
	//SequenceConfigs the list of sequence configs
	SequenceConfigs []SequenceConfig `yaml:"sequences"`
	queue           sequenceStack
	stop            bool
	sync.RWMutex
}

func (s *Sequencer) Init(ctx context.Context) {
	for _, sc := range s.SequenceConfigs {
		s.pushnew(sc)
	}
}

func (s *Sequencer) Roll(ctx context.Context, event *input.Event) {
	l := logger(ctx).With().
		Str("event_input", event.Input).
		Str("event_type", event.Type).
		Logger()
	s.RLock()
	defer s.RUnlock()
	if s.stop {
		l.Debug().Msg("Received event on stopped Sequencer")
		return
	}
	gclist := s.queue.GC(ctx)
	for _, seq := range gclist {
		s.pushnew(seq.sequenceConfig)
		l.Debug().Str("sequence_name", seq.sequenceConfig.Name).Msg("Cleaning timed out sequence")
	}

	sequences := s.queue.Match(ctx, s.DefaultInputs, event)
	for _, seq := range sequences {
		l = l.With().
			Str("sequence_name", seq.sequenceConfig.Name).
			Int("sequence_step", seq.step).
			Logger()
		ctx = l.WithContext(ctx)
		l.Debug().
			Msg("Event matched")

		// Running specified processor
		pc := seq.sequenceConfig.Steps[seq.step].Processor
		var next processor.NextStatus
		if pc.Type != "" && pc.Script != nil {
			var res interface{}
			res, next, _ = processor.Run(ctx, &pc, seq.payload)

			outputs := seq.sequenceConfig.Steps[seq.step].Outputs
			if len(outputs) == 0 {
				outputs = s.DefaultOutputs
			}
			//Sending requests to outputs
			response := s.sendToOutputs(ctx, pc.Encoding, outputs, event, res)
			seq.payload.Resp = response
		}

		//If this step is not last
		if next == processor.NextRepeatStep ||
			(seq.step < len(seq.sequenceConfig.Steps)-1 &&
				next != processor.NextStopSequence) {
			//Exporting variables
			for _, e := range seq.sequenceConfig.Steps[seq.step].Export {
				seq.payload.ExportField(ctx, e.Current, e.New)
				l.Debug().
					Str("export_current", e.Current).
					Str("export_new", e.New).
					Msg("Exported variable")
			}
			//Pushing sequence back to queue but with incremented step number
			if next != processor.NextRepeatStep {
				seq.step++
			}
			//Cleanup
			seq.payload.Req = nil
			seq.payload.Resp = nil
			//Pushing sequence back to queue
			s.queue.Push(seq)
			l.Debug().
				Msg("Next step")
		} else {
			//If it is last step starting sequence from beginning
			s.pushnew(seq.sequenceConfig)
			l.Debug().Msg("Sequence is finished. Creating new.")
		}
	}
}

func (s *Sequencer) Stop(ctx context.Context) {
	l := logger(ctx)
	l.Info().Msg("Shutting down sequencer")
	s.Lock()
	s.stop = true
	defer s.Unlock()
}

func (s *Sequencer) sendToOutputs(ctx context.Context, encoding string, outputs []output.OutputConfig, req *input.Event, res interface{}) (resp map[string]interface{}) {
	l := logger(ctx)
	// Decoding response

	switch encoding {
	case "none":
		var ok bool
		if resp, ok = res.(map[string]interface{}); !ok {
			l.Error().
				Msg("Wrong type of response returned from processor")
			return
		}
	case "json", "toml", "plain":
		var buf []byte
		switch v := res.(type) {
		case string:
			buf = []byte(v)
		case []byte:
			buf = v
		default:
			l.Error().
				Msg("Wrong type of response returned from processor")
			return
		}
		switch encoding {
		case "json":
			err := json.Unmarshal(buf, &resp)
			if err != nil {
				l.Error().
					Err(err).
					Msg("Cannot unmarshal JSON response returned from processor")
				return
			}
		case "toml":
			err := toml.Unmarshal(buf, &resp)
			if err != nil {
				l.Error().
					Err(err).
					Msg("Cannot unmarshal TOML response returned from processor")
				return
			}
		case "plain":
			resp = map[string]interface{}{
				"data": res,
			}
		}
	case "":
		l.Debug().
			Msg("Encoding is empty. Skipping response.")
		return
	default:
		l.Error().
			Msg("Wrong encoding")
		return
	}
	for _, o := range outputs {
		l = logger(ctx).With().Str("output_destination", o.Destination).
			Str("output_type", o.Type).
			Str("output_encoding", encoding).Logger()
		response := output.Response{
			ID:       req.ID,
			Request:  req,
			Type:     o.Type,
			Encoding: encoding,
			Data:     resp,
		}
		output.Send(ctx, o.Destination, &response)
	}
	return resp
}

func (s *Sequencer) pushnew(sc SequenceConfig) {
	seq := &Sequence{
		sequenceConfig: sc,
		payload:        &payload.Payload{Env: s.Env},
		step:           0,
	}
	s.queue.PushIfNotExists(seq)
}
