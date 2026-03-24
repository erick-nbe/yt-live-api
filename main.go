package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const PORT = 1212

var (
	reAllVideoID = regexp.MustCompile(`"videoId":"([A-Za-z0-9_-]{11,12})"`)

	// Usado apenas no debug
	reAllVideoDetails = regexp.MustCompile(`"videoDetails":\{"videoId":"(.*?)"`)

	reStreamTitle = regexp.MustCompile(`"title":\{"runs":\[\{"text":"(.*?)"`)
	reViewCountNearAssistindo = regexp.MustCompile(`"text":"([\d.,]+)"\},{"text":" (?:assistindo|watching|aguardando|esperando)"`)
	reUpcomingStartTime = regexp.MustCompile(`"upcomingEventData":\{"startTime":"(\d+)"`)
	reViewCount         = regexp.MustCompile(`"originalViewCount":"(\d+)"`)
)

type LiveData struct {
	VideoID            string
	Title              string
	ScheduledStartTime *int64
	ViewCount          *int64
}

func fetchHTML(channelID string) (html string, httpStatus int, finalURL string, err error) {
	targetURL := fmt.Sprintf("https://www.youtube.com/channel/%s/streams", channelID)

	req, err := http.NewRequest("GET", targetURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36,gzip(gfe)")
	req.Header.Set("Accept-Language", "pt-BR,pt;q=0.9,en-US;q=0.8,en;q=0.7")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	httpStatus = resp.StatusCode
	finalURL = resp.Request.URL.String()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	html = string(bodyBytes)
	return
}

func fetchLiveData(channelID string, getViews bool) (*LiveData, error) {
	html, _, _, err := fetchHTML(channelID)
	if err != nil {
		return nil, err
	}

	assistindoIdx := strings.Index(html, `" assistindo"`)
	if assistindoIdx == -1 {
		assistindoIdx = strings.Index(html, `" watching"`) // fallback ingles
	}
	if assistindoIdx == -1 {
		assistindoIdx = strings.Index(html, `" aguardando"`) // agendado portugues
	}
	if assistindoIdx == -1 {
		assistindoIdx = strings.Index(html, `" esperando"`) // agendado portugues 2
	}
	if assistindoIdx == -1 {
		assistindoIdx = strings.Index(html, `" waiting"`) // agendado ingles
	}
	if assistindoIdx == -1 {
		return nil, fmt.Errorf("nenhuma live encontrada ou ID não pôde ser verificado")
	}

	windowStart := assistindoIdx - 15000
	if windowStart < 0 {
		windowStart = 0
	}
	window := html[windowStart:assistindoIdx]

	var videoID string
	allMatches := reAllVideoID.FindAllStringSubmatch(window, -1)
	if len(allMatches) > 0 {
		videoID = allMatches[len(allMatches)-1][1]
	}
	if videoID == "" {
		return nil, fmt.Errorf("nenhuma live encontrada ou ID não pôde ser verificado")
	}

	var title string
	vidTagIdx := strings.LastIndex(window, `"videoId":"`+videoID+`"`)
	if vidTagIdx >= 0 {
		fwdStart := windowStart + vidTagIdx
		fwdEnd := fwdStart + 8000
		if fwdEnd > len(html) {
			fwdEnd = len(html)
		}
		if m := reStreamTitle.FindStringSubmatch(html[fwdStart:fwdEnd]); len(m) > 1 {
			title = m[1]
		}
	}

	var scheduledStartTime *int64
	for _, m := range reUpcomingStartTime.FindAllStringSubmatch(html, -1) {
		if len(m) > 1 {
			if t, err2 := strconv.ParseInt(m[1], 10, 64); err2 == nil {
				if scheduledStartTime == nil || t < *scheduledStartTime {
					v := t
					scheduledStartTime = &v
				}
			}
		}
	}

	viewWindow := html[windowStart : assistindoIdx+20]
	if assistindoIdx+20 > len(html) {
		viewWindow = html[windowStart:]
	}
	var viewCount *int64
	if getViews {
		if m := reViewCountNearAssistindo.FindStringSubmatch(viewWindow); len(m) > 1 {
			numStr := strings.ReplaceAll(strings.ReplaceAll(m[1], ".", ""), ",", "")
			if v, err2 := strconv.ParseInt(numStr, 10, 64); err2 == nil {
				viewCount = &v
			}
		}
		if viewCount == nil {
			if m := reViewCount.FindStringSubmatch(html); len(m) > 1 {
				if v, err2 := strconv.ParseInt(m[1], 10, 64); err2 == nil {
					viewCount = &v
				}
			}
		}
	}

	return &LiveData{
		VideoID:            videoID,
		Title:              title,
		ScheduledStartTime: scheduledStartTime,
		ViewCount:          viewCount,
	}, nil
}

func fetchScheduledStreams(channelID string) ([]LiveData, error) {
	html, _, _, err := fetchHTML(channelID)
	if err != nil {
		return nil, err
	}

	rendererTag := `"videoRenderer":{"videoId":"`
	parts := strings.Split(html, rendererTag)

	seen := map[string]bool{}
	var results []LiveData

	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if !strings.Contains(part, `"upcomingEventData"`) {
			continue
		}

		videoIDEnd := strings.IndexByte(part, '"')
		if videoIDEnd <= 0 || videoIDEnd > 12 {
			continue
		}
		videoID := part[:videoIDEnd]
		if seen[videoID] {
			continue
		}
		seen[videoID] = true

		var title string
		if m := reStreamTitle.FindStringSubmatch(part); len(m) > 1 {
			title = m[1]
		}

		var scheduledStartTime *int64
		if m := reUpcomingStartTime.FindStringSubmatch(part); len(m) > 1 {
			if t, err2 := strconv.ParseInt(m[1], 10, 64); err2 == nil {
				v := t
				scheduledStartTime = &v
			}
		}

		results = append(results, LiveData{
			VideoID:            videoID,
			Title:              title,
			ScheduledStartTime: scheduledStartTime,
		})
	}

	return results, nil
}

func ytLiveDebugHandler(w http.ResponseWriter, r *http.Request) {
	channelID := r.URL.Query().Get("id")
	if channelID == "" {
		http.Error(w, "É necessário fornecer o ID do canal como parâmetro.", http.StatusBadRequest)
		return
	}

	html, httpStatus, finalURL, err := fetchHTML(channelID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Erro ao buscar HTML: %v", err), http.StatusInternalServerError)
		return
	}

	allVideoDetailsMatches := []string{}
	for _, m := range reAllVideoDetails.FindAllStringSubmatch(html, -1) {
		if len(m) > 1 {
			allVideoDetailsMatches = append(allVideoDetailsMatches, m[1])
		}
	}

	seen := map[string]bool{}
	allVideoIDMatches := []string{}
	for _, m := range reAllVideoID.FindAllStringSubmatch(html, -1) {
		if len(m) > 1 && !seen[m[1]] {
			seen[m[1]] = true
			allVideoIDMatches = append(allVideoIDMatches, m[1])
		}
	}

	htmlSnippet := html
	if len(htmlSnippet) > 3000 {
		htmlSnippet = htmlSnippet[:3000]
	}

	resolvedVideoID := ""
	aidx := strings.Index(html, `" assistindo"`)
	if aidx == -1 {
		aidx = strings.Index(html, `" watching"`)
	}
	hasAssistindo := aidx >= 0
	if hasAssistindo {
		wStart := aidx - 15000
		if wStart < 0 {
			wStart = 0
		}
		dbMatches := reAllVideoID.FindAllStringSubmatch(html[wStart:aidx], -1)
		if len(dbMatches) > 0 {
			resolvedVideoID = dbMatches[len(dbMatches)-1][1]
		}
	}

	debug := map[string]any{
		"httpStatus":              httpStatus,
		"finalURL":                finalURL,
		"htmlSize":                len(html),
		"hasYtInitialPlayerResponse": strings.Contains(html, "ytInitialPlayerResponse"),
		"hasYtInitialData":        strings.Contains(html, "ytInitialData"),
		"hasConsentPage":          strings.Contains(html, "consent.youtube.com") || strings.Contains(html, "consentRequired"),
		"hasBotDetection":         strings.Contains(html, "Sorry") && strings.Contains(html, "unusual traffic"),
		"hasAssistindo":           hasAssistindo,
		"resolvedVideoID":         resolvedVideoID,
		"allVideoDetailsMatches":  allVideoDetailsMatches,
		"allUniqueVideoIDMatches": allVideoIDMatches,
		"htmlSnippet":             htmlSnippet,
	}

	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(debug)
}

func ytLiveHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	channelID := q.Get("id")
	includeScheduled := q.Get("scheduledStartTime") == "true"
	getViews := q.Get("getViews") == "true"
	listScheduled := q.Get("list") == "true"

	if channelID == "" {
		http.Error(w, "É necessário fornecer o ID do canal como parâmetro.", http.StatusBadRequest)
		return
	}

	if listScheduled {
		streams, err := fetchScheduledStreams(channelID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type ScheduledItem struct {
			VideoID            string `json:"video_id"`
			Title              string `json:"title"`
			ScheduledStartTime *int64 `json:"scheduledStartTime,omitempty"`
		}
		resp := make([]ScheduledItem, 0, len(streams))
		for _, s := range streams {
			resp = append(resp, ScheduledItem{
				VideoID:            s.VideoID,
				Title:              s.Title,
				ScheduledStartTime: s.ScheduledStartTime,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	data, err := fetchLiveData(channelID, getViews)
	if err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "nenhuma live") || strings.Contains(err.Error(), "não está ao vivo") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	responseObj := map[string]any{
		"video_id": data.VideoID,
		"title":    data.Title,
	}

	if getViews && data.ViewCount != nil {
		responseObj["viewCount"] = *data.ViewCount
	}
	if includeScheduled {
		if data.ScheduledStartTime != nil {
			responseObj["scheduledStartTime"] = *data.ScheduledStartTime
		} else {
			responseObj["scheduledStartTime"] = nil
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responseObj)
}

func main() {
	http.HandleFunc("/yt_live", ytLiveHandler)
	http.HandleFunc("/yt_live_debug", ytLiveDebugHandler)
	fmt.Printf("Server is running on http://localhost:%d\n", PORT)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", PORT), nil); err != nil {
		fmt.Printf("Erro ao iniciar o servidor: %v\n", err)
	}
}
