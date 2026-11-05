package web

// 本文件是运行时管理接口：直播、消息、配置、看门狗、事件详情与礼物流水。
//
// 写操作一律先 confirm=yes，再按白名单动作分发；返回的状态里不含凭据。

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
)

// handleLive 处理 /api/live：GET 状态，POST 启停看直播/直播采集。
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.control == nil {
			writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
			return
		}
		st := s.control.Status()
		writeJSON(w, http.StatusOK, map[string]any{"live": st.Live, "watch": st.Watch, "generated_at": st.GeneratedAt})
	case http.MethodPost:
		if s.control == nil {
			writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
			return
		}
		if !confirmYes(w, r, "修改直播设置") {
			return
		}
		var req struct {
			Action  string `json:"action"`
			Enabled bool   `json:"enabled"`
			RoomID  string `json:"room_id"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		var err error
		switch req.Action {
		case "collect":
			err = s.control.SetLiveCollect(req.Enabled)
		case "watch":
			err = s.control.SetWatch(req.Enabled, req.RoomID)
		default:
			writeError(w, http.StatusBadRequest, "action 只支持 collect/watch")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log.Info("面板修改直播设置",
			zap.String("action", "web_live_control"),
			zap.String("target", req.Action),
			zap.Bool("enabled", req.Enabled),
		)
		st := s.control.Status()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "live": st.Live, "watch": st.Watch})
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET/POST")
	}
}

// handleMessage 处理 /api/message：GET 状态，POST 启停评论/私信监听并改白名单。
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.control == nil {
			writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
			return
		}
		st := s.control.Status()
		writeJSON(w, http.StatusOK, map[string]any{"comment": st.Comment, "dm": st.DM, "generated_at": st.GeneratedAt})
	case http.MethodPost:
		if s.control == nil {
			writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
			return
		}
		if !confirmYes(w, r, "修改消息监听") {
			return
		}
		var req struct {
			Action      string   `json:"action"`
			Enabled     bool     `json:"enabled"`
			Videos      []string `json:"videos"`
			AllowedMIDs []string `json:"allowed_mids"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		var err error
		switch req.Action {
		case "comment":
			err = s.control.SetComment(req.Enabled, req.Videos)
		case "dm":
			err = s.control.SetDM(req.Enabled, req.AllowedMIDs)
		default:
			writeError(w, http.StatusBadRequest, "action 只支持 comment/dm")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log.Info("面板修改消息监听",
			zap.String("action", "web_message_control"),
			zap.String("target", req.Action),
			zap.Bool("enabled", req.Enabled),
		)
		st := s.control.Status()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "comment": st.Comment, "dm": st.DM})
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET/POST")
	}
}

// handleConfig 处理 /api/config：GET 常用开关快照，POST 按白名单字段热更新。
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if s.control == nil {
			writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
			return
		}
		st := s.control.Status()
		writeJSON(w, http.StatusOK, map[string]any{
			"proactive":    st.Proactive,
			"gift":         st.Gift,
			"memory":       st.Memory,
			"hot_reload":   st.HotReload,
			"meme_learn":   st.MemeLearn,
			"generated_at": st.GeneratedAt,
		})
	case http.MethodPost:
		if s.control == nil {
			writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
			return
		}
		if !confirmYes(w, r, "修改配置") {
			return
		}
		var req struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if err := s.applyConfigField(req.Field, req.Value); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.log.Info("面板修改运行时配置",
			zap.String("action", "web_config_update"),
			zap.String("field", req.Field),
		)
		st := s.control.Status()
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"proactive":  st.Proactive,
			"gift":       st.Gift,
			"memory":     st.Memory,
			"hot_reload": st.HotReload,
			"meme_learn": st.MemeLearn,
		})
	default:
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET/POST")
	}
}

// applyConfigField 只接受白名单字段；未知字段直接拒绝，避免变成任意配置写入口。
func (s *Server) applyConfigField(field string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("缺少 value")
	}
	switch field {
	case "proactive_enabled":
		value, err := decodeBool(raw)
		if err != nil {
			return err
		}
		return s.control.SetProactive(value, 0)
	case "proactive_interval_seconds":
		var value int
		if err := json.Unmarshal(raw, &value); err != nil {
			return errors.New("value 必须是整数秒")
		}
		if value < 5 || value > 3600 {
			return errors.New("主动弹幕间隔必须在5至3600秒之间")
		}
		// 只改间隔，不改变启停状态。
		enabled := s.control.Status().Proactive.Enabled
		return s.control.SetProactive(enabled, value)
	case "gift_enabled":
		value, err := decodeBool(raw)
		if err != nil {
			return err
		}
		return s.control.SetGift(value)
	case "memory_frozen":
		value, err := decodeBool(raw)
		if err != nil {
			return err
		}
		return s.control.SetMemoryFrozen(value)
	case "hot_reload_enabled":
		value, err := decodeBool(raw)
		if err != nil {
			return err
		}
		return s.control.SetHotReload(value)
	case "meme_learn_enabled":
		value, err := decodeBool(raw)
		if err != nil {
			return err
		}
		return s.control.SetMemeLearn(value)
	default:
		return errors.New("不支持的配置字段")
	}
}

func decodeBool(raw json.RawMessage) (bool, error) {
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("value 必须是布尔值")
	}
	return value, nil
}

// handleWatchdog 返回看门狗状态与重启历史（重启命令本身不下发）。
func (s *Server) handleWatchdog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if s.control == nil {
		writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
		return
	}
	writeJSON(w, http.StatusOK, s.control.Watchdog())
}

// handleCommentRecent 返回最近评论事件（in/out 可选）。
func (s *Server) handleCommentRecent(w http.ResponseWriter, r *http.Request) {
	s.handleChannelRecent(w, r, "comment")
}

// handleDMRecent 返回最近私信事件（in/out 可选）。
func (s *Server) handleDMRecent(w http.ResponseWriter, r *http.Request) {
	s.handleChannelRecent(w, r, "dm")
}

func (s *Server) handleChannelRecent(w http.ResponseWriter, r *http.Request, channel string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	limit := limitParam(r, defaultLimit)
	rows, err := s.repo.EventsByChannel(r.Context(), channel, r.URL.Query().Get("dir"), limit)
	if err != nil {
		if errors.Is(err, store.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "参数不合法（dir 只支持 in/out）")
			return
		}
		s.fail(w, "读取消息记录失败", err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		items = append(items, eventView(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items), "channel": channel})
}

// handleEventDetail 返回单条事件的完整上下文、回复计划与投递尝试。
func (s *Server) handleEventDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/events/")
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") || len(id) > 128 {
		writeError(w, http.StatusBadRequest, "缺少事件 id")
		return
	}
	detail, err := s.repo.Event(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrWebNotFound) {
			writeError(w, http.StatusNotFound, "事件不存在")
			return
		}
		if errors.Is(err, store.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "事件 id 不合法")
			return
		}
		s.fail(w, "读取事件详情失败", err)
		return
	}
	payload := eventView(detail.WebEvent)
	payload["from_id"] = detail.FromID
	payload["reply_to"] = clipN(detail.ReplyTo, maxDetailRunes)
	payload["private"] = detail.Private
	payload["meta"] = clipN(detail.Meta, maxMetaRunes)
	payload["created_at"] = detail.CreatedAt.UTC().Format(timeLayout)
	payload["text"] = clipN(detail.Text, maxDetailRunes)
	payload["from_name"] = clipN(detail.FromName, maxDetailRunes)
	if detail.Reply != nil {
		reply := map[string]any{
			"actor_id":      detail.Reply.ActorID,
			"room_id":       detail.Reply.RoomID,
			"text":          clipN(detail.Reply.Text, maxDetailRunes),
			"state":         detail.Reply.State,
			"attempt_count": detail.Reply.AttemptCount,
			"created_at":    detail.Reply.CreatedAt.UTC().Format(timeLayout),
			"attempts":      make([]map[string]any, 0, len(detail.Reply.Attempts)),
		}
		attempts := reply["attempts"].([]map[string]any)
		for _, a := range detail.Reply.Attempts {
			attempts = append(attempts, map[string]any{
				"number":     a.Number,
				"state":      a.State,
				"ok":         a.OK,
				"verified":   a.Verified,
				"msg_id":     clipN(a.MsgID, 128),
				"evidence":   clipN(a.Evidence, maxDetailRunes),
				"error_kind": a.ErrorKind,
				"created_at": a.CreatedAt.UTC().Format(timeLayout),
			})
		}
		reply["attempts"] = attempts
		payload["reply"] = reply
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleGiftQuota 返回送礼开关与当日额度。
func (s *Server) handleGiftQuota(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "只支持 GET")
		return
	}
	if s.control == nil {
		writeError(w, http.StatusServiceUnavailable, "运行时控制未启用")
		return
	}
	writeJSON(w, http.StatusOK, s.control.Status().Gift)
}

// eventView 是列表/详情共用的事件投影（正文截断）。
func eventView(e store.WebEvent) map[string]any {
	return map[string]any{
		"id":        e.ID,
		"channel":   e.Channel,
		"dir":       e.Dir,
		"session":   e.Session,
		"from_name": clip(e.FromName),
		"text":      clip(e.Text),
		"at":        e.At.UTC().Format(timeLayout),
		"status":    e.Status,
	}
}

// decodeJSON 用严格字段解析请求体；失败时已写 400。
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "请求体必须是合法 JSON")
		return false
	}
	return true
}

// 详情页展示上限：足够看清上下文，又不至于把整段 prompt 推给浏览器。
const (
	maxDetailRunes = 2000
	maxMetaRunes   = 4000
	timeLayout     = "2006-01-02T15:04:05Z07:00"
)

// clipN 按 rune 截断；n<=0 时用列表默认值。
func clipN(text string, limit int) string {
	if limit <= 0 {
		limit = maxTextRunes
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
