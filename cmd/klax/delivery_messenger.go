package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/PiDmitrius/klax/internal/mdhtml"
	"github.com/PiDmitrius/klax/internal/runner"
	"github.com/PiDmitrius/klax/internal/transport"
)

// messengerDelivery streams a turn to a Telegram/MAX/VK chat. It is the
// edit-streaming machinery that used to live inline in runBackend, moved behind
// the Delivery interface verbatim: it sets up the progress message chain
// (reusing the "В очереди" notification when nothing else happened in the chat),
// runs a rate-limited worker that edits that chain as cumulative progress
// snapshots arrive, and on Final renders the answer (or error) and flushes it
// to the chain. Nothing about its behaviour changed in the extraction.
type messengerDelivery struct {
	d              *daemon
	ctx            context.Context // run context; /abort cancels it to unblock edits
	chatID         string
	replyTo        string
	chatFmt        string
	verbose        bool
	hasTransport   bool
	sessionKey     string
	sessionCreated int64

	// chain is the progress/answer message chain. Only the worker mutates it
	// until stopWorker() returns; Final reads it afterwards (no race).
	chain *messageChain

	logItems     []runner.ProgressEvent // accumulated by Progress, read by Final
	progressCh   chan []runner.ProgressEvent
	progressDone chan struct{}
	workerDone   chan struct{}
	stopOnce     sync.Once
}

func (d *daemon) newMessengerDelivery(ctx context.Context, msg queuedMsg, verbose bool) *messengerDelivery {
	t, _, _ := d.transportFor(msg.chatID)
	chatFmt := d.answerFormat(msg.chatID)
	// Rich messages have their own message type: a Rich Message can only be
	// edit-streamed from a message that was *born* rich (editMessageText with
	// rich_message). So in rich mode we never reuse the plain/HTML queued
	// notification, and the placeholder itself is created as rich.
	richMode := chatFmt == "rich"

	m := &messengerDelivery{
		d:              d,
		ctx:            ctx,
		chatID:         msg.chatID,
		replyTo:        msg.msgID,
		chatFmt:        chatFmt,
		verbose:        verbose,
		hasTransport:   t != nil,
		sessionKey:     msg.sessKey,
		sessionCreated: msg.sessCreated,
		progressCh:     make(chan []runner.ProgressEvent, 1),
		progressDone:   make(chan struct{}),
		workerDone:     make(chan struct{}),
	}

	// Progress message — edit in place. If this message was queued and nothing
	// happened in the chat since then, reuse the queue notification. Otherwise
	// point to the new answer below.
	var progressChain *messageChain
	reuseQueuedProgress := !richMode && d.shouldReuseQueuedProgress(msg)
	needsRedirectMarker := !reuseQueuedProgress && msg.progressID != ""
	if reuseQueuedProgress {
		progressChain = newMessageChain(msg.progressID)
		// The queued placeholder was created (in enqueueToSession) as a reply
		// to msg.msgID — record that so its first edit here resends it too
		// (ym needs reply_message_id on every edit or it drops the link).
		progressChain.replyTos[msg.progressID] = msg.msgID
		progressChain.lastCreateActivity = msg.progressSeq
	}
	if t != nil {
		var err error
		placeholder, placeFmt := "...", ""
		if richMode {
			placeholder, placeFmt = "<p>…</p>", "rich"
		}
		progressChain, err = d.syncMessageChain(ctx, msg.chatID, msg.msgID, progressChain, placeholder, placeFmt)
		if err != nil {
			progressChain = nil
		} else if needsRedirectMarker {
			markerCtx, markerCancel := withDeliveryTimeout(ctx)
			_, _ = d.performTransportOp(markerCtx, transportOp{
				fullChatID: msg.chatID,
				messageID:  msg.progressID,
				replyTo:    msg.msgID,
				text:       "↓",
				format:     "",
			})
			markerCancel()
		}
	}
	m.chain = progressChain
	m.startWorker()
	return m
}

// startWorker runs the rate-limited edit loop. onProgress hands it cumulative
// logItems snapshots via the mailbox channel and this goroutine — not the
// runner's stdout goroutine — does the network edits, so reading stdout never
// blocks on Telegram latency.
func (m *messengerDelivery) startWorker() {
	d := m.d
	go func() {
		defer close(m.workerDone)
		var lastSentText string
		for {
			var snapshot []runner.ProgressEvent
			select {
			case <-m.progressDone:
				return
			case s, ok := <-m.progressCh:
				if !ok {
					return
				}
				snapshot = s
			}
			select {
			case <-m.progressDone:
				return
			default:
			}
			chunks := withProgressEllipsis(formatLogChunks(snapshot, "", m.chatFmt, maxMessageLen), m.chatFmt, maxMessageLen)
			cacheKey := fmt.Sprintf("%q", chunks)
			if cacheKey == lastSentText {
				continue
			}
			lastSentText = cacheKey
			if m.chain != nil && len(m.chain.ids) > 0 {
				pc, err := d.syncMessageChainChunks(m.ctx, m.chatID, m.replyTo, m.chain, chunks, m.chatFmt)
				if err != nil {
					log.Printf("progress update failed: %v", err)
					continue
				}
				m.chain = pc
			}
			// Rate-limit edits so Telegram does not 429 us. Cancellation
			// shortcuts the wait so /abort unblocks quickly.
			select {
			case <-m.progressDone:
				return
			case <-m.ctx.Done():
			case <-time.After(progressEditInterval):
			}
		}
	}()
}

// Progress is the runner.ProgressFunc. It runs in the stdout-scanner goroutine
// and only ever appends and does a non-blocking mailbox send — never network.
func (m *messengerDelivery) Progress(ev runner.ProgressEvent) {
	if ev.Kind == runner.ProgressKindContext {
		return
	}
	if !m.verbose {
		return
	}
	// No upstream dedup here on purpose: the progress worker already suppresses
	// duplicate edits via its lastSentText check, and collapsing equal
	// ProgressEvents at this level would hide real repeats (same tool invoked
	// twice, same rate-limit warning reappearing after a cooldown).
	m.logItems = append(m.logItems, ev)
	snapshot := append([]runner.ProgressEvent(nil), m.logItems...)
	// Non-blocking mailbox: drop any stale pending snapshot in favour of the
	// newer, superset one. Never blocks the scanner.
	select {
	case m.progressCh <- snapshot:
	default:
		select {
		case <-m.progressCh:
		default:
		}
		select {
		case m.progressCh <- snapshot:
		default:
		}
	}
}

func (m *messengerDelivery) Warning(text string) {
	m.logItems = append(m.logItems, runner.ProgressEvent{
		Kind: runner.ProgressKindTool,
		Text: "⚠️ " + text,
	})
}

// stopWorker flushes the progress worker: the worker mutates chain, so any
// final-delivery path that reads chain must run this barrier first. Idempotent.
func (m *messengerDelivery) stopWorker() {
	m.stopOnce.Do(func() {
		close(m.progressDone)
		close(m.progressCh)
		<-m.workerDone
	})
}

func (m *messengerDelivery) Final(res runner.RunResult) {
	m.stopWorker()
	d := m.d

	if res.Error != nil {
		finalChunks := formatRunFailureChunks(m.logItems, m.chatFmt, res.Error)
		if m.hasTransport {
			// Deliver with chatFmt so a rich-formatted failure goes out as a Rich
			// Message (and reuses the rich-born progress chain when present).
			if _, err := d.syncFinalMessageChainChunks(m.chatID, m.replyTo, m.chain, finalChunks, m.chatFmt); err != nil {
				log.Printf("final error delivery failed: %v", err)
			}
		} else {
			d.sendMessage(m.chatID, m.replyTo, strings.Join(finalChunks, "\n\n"))
		}
		return
	}

	text := strings.TrimSpace(res.Text)
	if text == "" {
		text = "✅"
	}

	// Convert Claude's Markdown to the transport format.
	var formatted string
	switch m.chatFmt {
	case "rich":
		formatted = mdhtml.ConvertRich(text)
	case "html":
		// Telegram parse_mode=HTML supports <blockquote>; MAX's support is
		// unverified, so it keeps the legacy <pre> for quotes.
		formatted = mdhtml.Convert(text, transportPrefix(m.chatID) == "tg")
	default:
		formatted = text
	}

	// Build final message: progress log + separator + answer.
	var finalChunks []string
	if len(m.logItems) > 0 {
		finalChunks = formatLogChunks(m.logItems, formatted, m.chatFmt, maxMessageLen)
	} else {
		finalChunks = splitMessage(formatted, maxMessageLen, m.chatFmt)
	}

	if m.hasTransport {
		if _, err := d.syncFinalMessageChainChunks(m.chatID, m.replyTo, m.chain, finalChunks, m.chatFmt); err != nil {
			log.Printf("final delivery failed: %v", err)
		}
		m.sendOutboundFiles(text)
	}
}

func (m *messengerDelivery) sendOutboundFiles(answer string) {
	if !m.d.outboundFilesEnabled() {
		return
	}
	t, rawChatID, _ := m.d.transportFor(m.chatID)
	sender, ok := t.(transport.FileSender)
	if !ok {
		return
	}
	sess := m.d.store.Get(m.msgSessionKey(), m.msgSessionCreated())
	if sess == nil {
		return
	}
	var failed []string
	for _, ref := range outboundFileRefs(answer, sess.CWD) {
		// One file is in memory at a time: read it, send it, drop it.
		contentType, data, err := ref.load()
		if err != nil {
			log.Printf("file delivery skipped (%s): %v", ref.name, err)
			failed = append(failed, ref.name)
			continue
		}
		// Same retry policy as every other send: a 429 on sendDocument is
		// routine when an answer carries several files, and dropping the
		// attachment silently leaves the user with a link to nothing.
		err = retryDo(m.ctx, func() error {
			return sender.SendFile(rawChatID, ref.name, contentType, data, "", m.replyTo)
		})
		if err != nil {
			log.Printf("file delivery failed (%s): %v", ref.name, err)
			if m.ctx.Err() == nil { // /abort is the user's own doing, not a failure to report
				failed = append(failed, ref.name)
			}
		}
	}
	if len(failed) > 0 {
		m.d.sendMessage(m.chatID, m.replyTo, "⚠️ не удалось отправить файлом: "+strings.Join(failed, ", "))
	}
}

// These accessors are overridden by the delivery's bound message in the
// constructor; kept as methods so file delivery cannot accidentally use the
// current active session after /switch or /new.
func (m *messengerDelivery) msgSessionKey() string    { return m.sessionKey }
func (m *messengerDelivery) msgSessionCreated() int64 { return m.sessionCreated }

func (m *messengerDelivery) Close() {
	m.stopWorker()
}
