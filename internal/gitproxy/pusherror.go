package gitproxy

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strconv"
	"time"
)

// pushForm is how an error reaches git on a request. git reads the result of a push (POST git-receive-pack)
// through the side-band channels when it negotiated them, so a plain ERR line there ends in "bad band".
type pushForm int

const (
	// formERR is an ERR pkt-line: the advertisement, upload-pack, and the probe of a large push, which carries
	// no command yet and ignores the answer.
	formERR pushForm = iota
	// formSideband is the text on channel 3. git prints it as the remote error.
	formSideband
	// formStatus is a non-200 status. A push without side-band reads nothing else.
	formStatus
)

// pushFormOf looks at the first command of a push without consuming it: "<old> <new> <ref>\0<capabilities>".
// It replaces r.Body with a reader that still delivers every byte. Anything it cannot read in peekTimeout is formERR.
func pushFormOf(r *http.Request, rc *http.ResponseController) pushForm {
	br := bufio.NewReaderSize(r.Body, 64<<10)
	r.Body = struct {
		io.Reader
		io.Closer
	}{br, r.Body}
	if r.Header.Get("Content-Encoding") != "" { // git does not compress a push; do not guess
		return formERR
	}
	_ = rc.SetReadDeadline(time.Now().Add(peekTimeout))
	defer func() { _ = rc.SetReadDeadline(time.Time{}) }()
	hdr, err := br.Peek(4)
	if err != nil {
		return formERR
	}
	n, err := strconv.ParseUint(string(hdr), 16, 16)
	if err != nil || n <= 4 { // a flush: the probe of a large push
		return formERR
	}
	line, err := br.Peek(int(n))
	if err != nil {
		return formERR
	}
	_, caps, _ := bytes.Cut(line[4:], []byte{0})
	if bytes.Contains(caps, []byte("side-band")) {
		return formSideband
	}
	return formStatus
}

// errorAnswer is status, content type and body of an error answer. code is the status of formStatus.
func errorAnswer(t Target, form pushForm, code int, msg string) (int, string, []byte) {
	if t.Op == OpReceivePack {
		switch form {
		case formSideband:
			return http.StatusOK, contentType(t), []byte(pktLine("\x03"+cleanMessage(msg)+"\n") + "0000")
		case formStatus:
			return code, "text/plain; charset=utf-8", []byte(cleanMessage(msg) + "\n")
		}
	}
	return http.StatusOK, contentType(t), errorBody(t, msg)
}
