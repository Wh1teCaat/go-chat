// loadtest exercises real HTTP/WS endpoints; use only an isolated test database.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type config struct {
	URLs            string
	Pairs, Messages int
	Rate            float64
	Spread          bool
	Drain           time.Duration
	Output          string
}
type account struct {
	Token string
	ID    uint
}
type payload struct {
	Run    string `json:"run"`
	Sender int    `json:"sender"`
	Seq    int    `json:"seq"`
}
type envelope struct {
	Type string `json:"type"`
	Data struct {
		ID          uint   `json:"id"`
		Seq         uint64 `json:"seq"`
		MessageID   uint   `json:"messageID"`
		ClientMsgID string `json:"clientMsgID"`
		Content     string `json:"content"`
	} `json:"data"`
}
type observation struct {
	seen                                                                       map[int]bool
	seq                                                                        map[int]int
	maxID                                                                      uint
	maxConversationSeq                                                         uint64
	Duplicates, SequenceRegressions, IDRegressions, ConversationSeqRegressions int
	Examples                                                                   []string
}

func (o *observation) add(key, sender, seq int, id uint, conversationSeq uint64) bool {
	if o.seen == nil {
		o.seen = map[int]bool{}
		o.seq = map[int]int{}
	}
	if o.seen[key] {
		o.Duplicates++
		return false
	}
	o.seen[key] = true
	if seq < o.seq[sender] {
		o.SequenceRegressions++
	}
	if seq > o.seq[sender] {
		o.seq[sender] = seq
	}
	if id < o.maxID {
		o.IDRegressions++
		if len(o.Examples) < 5 {
			o.Examples = append(o.Examples, fmt.Sprintf("previous_max_id=%d arrived_id=%d sender=%d seq=%d", o.maxID, id, sender, seq))
		}
	}
	if id > o.maxID {
		o.maxID = id
	}
	if conversationSeq < o.maxConversationSeq {
		o.ConversationSeqRegressions++
		if len(o.Examples) < 5 {
			o.Examples = append(o.Examples, fmt.Sprintf("previous_max_conversation_seq=%d arrived_seq=%d sender=%d sender_seq=%d", o.maxConversationSeq, conversationSeq, sender, seq))
		}
	}
	if conversationSeq > o.maxConversationSeq {
		o.maxConversationSeq = conversationSeq
	}
	return true
}

type latency struct {
	Count              int `json:"count"`
	P50, P95, P99, Max float64
}

func summarize(v []float64) latency {
	if len(v) == 0 {
		return latency{}
	}
	sort.Float64s(v)
	q := func(p float64) float64 { return v[int(math.Ceil(p*float64(len(v))))-1] }
	return latency{len(v), q(.5), q(.95), q(.99), v[len(v)-1]}
}

type report struct {
	Run                                                                                                                                                                                                                                                                         string
	Config                                                                                                                                                                                                                                                                      config
	StartedUTC                                                                                                                                                                                                                                                                  string
	Connections                                                                                                                                                                                                                                                                 int
	Upstreams                                                                                                                                                                                                                                                                   map[string]int
	CrossInstancePairs                                                                                                                                                                                                                                                          int
	SendSeconds, ObservationSeconds, SuccessfulWritesPerSecond, DeliveriesPerSecond                                                                                                                                                                                             float64
	Planned, SuccessfulWrites, WriteErrors, BusinessErrors, UnexpectedReadErrors, ACKs, MissingACKs, PeerDeliveries, MissingPeerDeliveries, MissingSelfEchoes, DuplicateACKs, DuplicatePushes, SenderSequenceRegressions, ConversationIDRegressions, ConversationSeqRegressions int
	ACKMilliseconds, PeerMilliseconds                                                                                                                                                                                                                                           latency
	OrderExamples                                                                                                                                                                                                                                                               []string
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

func post(base, path, token string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d (check test rate limits)", path, resp.StatusCode)
	}
	var e struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(e.Data, out)
	}
	return nil
}
func setup(base, run string, i int) ([2]account, error) {
	var a [2]account
	emails := [2]string{fmt.Sprintf("bench-%s-%d-a@test.com", run, i), fmt.Sprintf("bench-%s-%d-b@test.com", run, i)}
	for j := range a {
		body := map[string]any{"email": emails[j], "password": "bench-pass-123", "nickname": "loadtest"}
		if err := post(base, "/v1/user/register", "", body, nil); err != nil {
			return a, err
		}
		var l struct {
			Token string `json:"token"`
		}
		if err := post(base, "/v1/user/login", "", body, &l); err != nil {
			return a, err
		}
		if l.Token == "" {
			return a, fmt.Errorf("empty login token")
		}
		a[j].Token = l.Token
	}
	if err := post(base, "/v1/friend/add", a[0].Token, map[string]any{"friendEmail": emails[1]}, nil); err != nil {
		return a, err
	}
	var pending []struct {
		RequestID uint `json:"requestID"`
	}
	if err := post(base, "/v1/friend/pending", a[1].Token, struct{}{}, &pending); err != nil {
		return a, err
	}
	if len(pending) != 1 {
		return a, fmt.Errorf("expected one friend request")
	}
	if err := post(base, "/v1/friend/accept", a[1].Token, map[string]any{"requestID": pending[0].RequestID}, nil); err != nil {
		return a, err
	}
	for j := range a {
		var friends []struct {
			UserID uint `json:"userID"`
		}
		if err := post(base, "/v1/friend/list", a[j].Token, struct{}{}, &friends); err != nil {
			return a, err
		}
		if len(friends) != 1 {
			return a, fmt.Errorf("expected one friend")
		}
		a[1-j].ID = friends[0].UserID
	}
	// Materialize the conversation before concurrent sends (excluded from measurement).
	err := post(base, "/v1/message/list", a[0].Token, map[string]any{"targetType": "private", "targetID": a[1].ID}, nil)
	return a, err
}
func run(c config) (report, error) {
	r := report{Run: fmt.Sprintf("%x", time.Now().UnixNano()), Config: c, StartedUTC: time.Now().UTC().Format(time.RFC3339), Upstreams: map[string]int{}}
	urls := strings.Split(c.URLs, ",")
	for i := range urls {
		urls[i] = strings.TrimRight(strings.TrimSpace(urls[i]), "/")
	}
	n := c.Pairs * 2
	r.Planned = n * c.Messages
	accounts := make([]account, n)
	jobs := make(chan int)
	errs := make(chan error, c.Pairs)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				a, err := setup(urls[0], r.Run, p)
				if err != nil {
					errs <- err
					continue
				}
				accounts[p*2] = a[0]
				accounts[p*2+1] = a[1]
			}
		}()
	}
	for p := 0; p < c.Pairs; p++ {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	close(errs)
	for err := range errs {
		return r, fmt.Errorf("setup: %w", err)
	}
	fmt.Fprintf(os.Stderr, "prepared %d accounts; connecting websockets\n", n)
	conns := make([]*websocket.Conn, n)
	up := make([]string, n)
	defer func() {
		for _, conn := range conns {
			if conn != nil {
				conn.Close()
			}
		}
	}()
	for i := range conns {
		base := urls[i%len(urls)]
		d := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Subprotocols: []string{"chat", "bearer." + accounts[i].Token}}
		conn, resp, err := d.Dial(strings.Replace(base, "http", "ws", 1)+"/v1/ws", nil)
		if err != nil {
			return r, fmt.Errorf("dial %d: %w", i, err)
		}
		conns[i] = conn
		up[i] = resp.Header.Get("X-Upstream-Addr")
		if up[i] == "" && len(urls) > 1 {
			up[i] = base
		}
		r.Upstreams[up[i]]++
		r.Connections++
	}
	for p := 0; p < c.Pairs; p++ {
		if up[2*p] != "" && up[2*p+1] != "" && up[2*p] != up[2*p+1] {
			r.CrossInstancePairs++
		}
	}
	if r.CrossInstancePairs == 0 {
		return r, fmt.Errorf("no proven cross-instance pair: pass distinct direct backend URLs, or use edge X-Upstream-Addr")
	}
	starts := make([]time.Time, r.Planned)
	written := make([]bool, r.Planned)
	acks := make([]bool, r.Planned)
	peer := make([]bool, r.Planned)
	self := make([]bool, r.Planned)
	obs := make([]observation, n)
	var mu sync.Mutex
	var stopping atomic.Bool
	var readers sync.WaitGroup
	var ackLat, peerLat []float64
	for i, conn := range conns {
		readers.Add(1)
		go func(i int, conn *websocket.Conn) {
			defer readers.Done()
			for {
				var e envelope
				if err := conn.ReadJSON(&e); err != nil {
					if !stopping.Load() {
						mu.Lock()
						r.UnexpectedReadErrors++
						mu.Unlock()
					}
					return
				}
				now := time.Now()
				mu.Lock()
				if e.Type == "error" {
					r.BusinessErrors++
				}
				if e.Type == "message_ack" {
					var sender, seq int
					if _, err := fmt.Sscanf(e.Data.ClientMsgID, r.Run+"-%d-%d", &sender, &seq); err == nil && sender == i && seq > 0 && seq <= c.Messages {
						k := sender*c.Messages + seq - 1
						if !starts[k].IsZero() {
							if acks[k] {
								r.DuplicateACKs++
							} else {
								acks[k] = true
								ackLat = append(ackLat, float64(now.Sub(starts[k]))/1e6)
							}
						}
					}
				}
				if e.Type == "message" {
					var p payload
					if json.Unmarshal([]byte(e.Data.Content), &p) == nil && p.Run == r.Run && p.Sender >= 0 && p.Sender < n && p.Sender/2 == i/2 && p.Seq > 0 && p.Seq <= c.Messages {
						k := p.Sender*c.Messages + p.Seq - 1
						if !starts[k].IsZero() && obs[i].add(k, p.Sender, p.Seq, e.Data.ID, e.Data.Seq) {
							if p.Sender == i {
								self[k] = true
							} else {
								peer[k] = true
								peerLat = append(peerLat, float64(now.Sub(starts[k]))/1e6)
							}
						}
					}
				}
				mu.Unlock()
			}
		}(i, conn)
	}
	begin := time.Now()
	fmt.Fprintf(os.Stderr, "load: %d connections, %d cross-instance pairs, %.1f offered messages/s\n", n, r.CrossInstancePairs, float64(n)*c.Rate)
	for i, conn := range conns {
		wg.Add(1)
		go func(i int, conn *websocket.Conn) {
			defer wg.Done()
			// Spread each connection over one send interval, so the requested total
			// rate is steady instead of an artificial all-connection burst.
			phase := 0.0
			if c.Spread {
				phase = float64(i) / float64(n) / c.Rate
			}
			for seq := 1; seq <= c.Messages; seq++ {
				due := begin.Add(time.Duration((phase + float64(seq-1)/c.Rate) * float64(time.Second)))
				if wait := time.Until(due); wait > 0 {
					time.Sleep(wait)
				}
				k := i*c.Messages + seq - 1
				b, _ := json.Marshal(payload{r.Run, i, seq})
				mu.Lock()
				starts[k] = time.Now()
				mu.Unlock()
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				err := conn.WriteJSON(map[string]any{"type": "message", "clientMsgID": fmt.Sprintf("%s-%d-%d", r.Run, i, seq), "targetType": "private", "targetID": accounts[i^1].ID, "content": string(b)})
				mu.Lock()
				if err != nil {
					r.WriteErrors++
					mu.Unlock()
					return
				}
				written[k] = true
				r.SuccessfulWrites++
				mu.Unlock()
			}
		}(i, conn)
	}
	wg.Wait()
	r.SendSeconds = time.Since(begin).Seconds()
	time.Sleep(c.Drain)
	r.ObservationSeconds = time.Since(begin).Seconds()
	stopping.Store(true)
	for _, conn := range conns {
		conn.Close()
	}
	readers.Wait()
	for k, ok := range written {
		if !ok {
			continue
		}
		if acks[k] {
			r.ACKs++
		} else {
			r.MissingACKs++
		}
		if peer[k] {
			r.PeerDeliveries++
		} else {
			r.MissingPeerDeliveries++
		}
		if !self[k] {
			r.MissingSelfEchoes++
		}
	}
	for i, o := range obs {
		r.DuplicatePushes += o.Duplicates
		r.SenderSequenceRegressions += o.SequenceRegressions
		r.ConversationIDRegressions += o.IDRegressions
		r.ConversationSeqRegressions += o.ConversationSeqRegressions
		for _, s := range o.Examples {
			if len(r.OrderExamples) < 10 {
				r.OrderExamples = append(r.OrderExamples, fmt.Sprintf("receiver=%d %s", i, s))
			}
		}
	}
	r.ACKMilliseconds = summarize(ackLat)
	r.PeerMilliseconds = summarize(peerLat)
	r.SuccessfulWritesPerSecond = float64(r.SuccessfulWrites) / r.SendSeconds
	r.DeliveriesPerSecond = float64(r.PeerDeliveries) / r.ObservationSeconds
	return r, nil
}
func main() {
	var c config
	flag.StringVar(&c.URLs, "urls", "http://localhost:8080", "comma-separated direct backends, or edge with X-Upstream-Addr")
	flag.IntVar(&c.Pairs, "pairs", 10, "private conversations; two concurrent senders/connections each")
	flag.IntVar(&c.Messages, "messages", 100, "messages per sender")
	flag.Float64Var(&c.Rate, "rate", 10, "messages/sec per sender (paced, no ACK wait)")
	flag.BoolVar(&c.Spread, "spread", false, "spread connections evenly within each send interval")
	flag.DurationVar(&c.Drain, "drain", 10*time.Second, "post-send observation window")
	flag.StringVar(&c.Output, "output", "", "JSON report path")
	flag.Parse()
	if c.Pairs < 1 || c.Messages < 1 || c.Rate <= 0 || math.IsNaN(c.Rate) || math.IsInf(c.Rate, 0) || c.Drain < 0 {
		fmt.Fprintln(os.Stderr, "invalid load parameters")
		os.Exit(2)
	}
	r, err := run(c)
	b, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(b))
	if c.Output != "" {
		if e := os.WriteFile(c.Output, append(b, '\n'), 0644); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(2)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if r.WriteErrors+r.BusinessErrors+r.UnexpectedReadErrors+r.MissingACKs+r.MissingPeerDeliveries+r.MissingSelfEchoes+r.DuplicatePushes+r.DuplicateACKs+r.SenderSequenceRegressions+r.ConversationSeqRegressions > 0 {
		os.Exit(1)
	}
}
