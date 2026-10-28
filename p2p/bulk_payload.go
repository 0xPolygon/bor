package p2p

import "time"

type bulkPayloadReader struct {
	stream  bulkFrameStream
	started bool
}

func (p *bulkPayloadReader) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}
	if !p.started {
		// Start after memory admission, but do not extend the deadline when a
		// peer trickles a payload across multiple reads.
		if err := p.stream.SetReadDeadline(time.Now().Add(bulkMessageReadTimeout)); err != nil {
			return 0, err
		}
		p.started = true
	}
	return p.stream.Read(buf)
}
