package glance

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Constants
const videosWidgetPlaylistPrefix = "playlist:"

// Template variables
var (
	videosWidgetTemplate             = mustParseTemplate("videos.html", "widget-base.html", "video-card-contents.html")
	videosWidgetGridTemplate         = mustParseTemplate("videos-grid.html", "widget-base.html", "video-card-contents.html")
	videosWidgetVerticalListTemplate = mustParseTemplate("videos-vertical-list.html", "widget-base.html")
)

// =============================================================================
// TYPE DEFINITIONS
// =============================================================================

// videosWidget represents the main video widget structure
type videosWidget struct {
	widgetBase        `yaml:",inline"`
	Videos            videoList `yaml:"-"`
	VideoUrlTemplate  string    `yaml:"video-url-template"`
	Style             string    `yaml:"style"`
	CollapseAfter     int       `yaml:"collapse-after"`
	CollapseAfterRows int       `yaml:"collapse-after-rows"`
	Channels          []string  `yaml:"channels"`
	RumbleChannels    []string  `yaml:"rumble-channels"`
	Playlists         []string  `yaml:"playlists"`
	Limit             int       `yaml:"limit"`
	IncludeShorts     bool      `yaml:"include-shorts"`
	
	// Add flag to track if this is the first load
	isFirstLoad       bool      `yaml:"-"`
}

// video represents a single video entry
type video struct {
	ThumbnailUrl string
	Title        string
	Url          string
	Author       string
	AuthorUrl    string
	TimePosted   time.Time
}

// videoList represents a collection of videos
type videoList []video

// rumbleVideo represents a single Rumble video entry
type rumbleVideo struct {
	ThumbnailUrl string
	Title        string
	Url          string
	Author       string
	AuthorUrl    string
	TimePosted   time.Time
}

// rumbleVideoList represents a collection of Rumble videos
type rumbleVideoList []rumbleVideo

// YouTube API response structures
type youtubeFeedResponseXml struct {
	Channel     string `xml:"author>name"`
	ChannelLink string `xml:"author>uri"`
	Videos      []struct {
		Title     string `xml:"title"`
		Published string `xml:"published"`
		Link      struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`

		Group struct {
			Thumbnail struct {
				Url string `xml:"url,attr"`
			} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
		} `xml:"http://search.yahoo.com/mrss/ group"`
	} `xml:"entry"`
}

// Rumble API response structures
type rumbleFeedResponseXml struct {
	Channel     string `xml:"channel>title"`
	ChannelLink string `xml:"channel>link"`
	Videos      []struct {
		Title     string `xml:"title"`
		Published string `xml:"pubDate"`
		Link      string `xml:"guid"`
		Thumbnail struct {
			Url string `xml:"url,attr"`
		} `xml:"itunes:image"`
		MediaThumbnail struct {
			Url string `xml:"url,attr"`
		} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
	} `xml:"channel>item"`
}

// =============================================================================
// VIDEOS WIDGET METHODS
// =============================================================================

// initialize sets up the videos widget with default values
func (widget *videosWidget) initialize() error {
	// Set initial cache duration - will be extended after first successful fetch
	widget.withTitle("Videos").withCacheDuration(1 * time.Minute)

	if widget.Limit <= 0 {
		widget.Limit = 25
	}

	if widget.CollapseAfterRows == 0 || widget.CollapseAfterRows < -1 {
		widget.CollapseAfterRows = 4
	}

	if widget.CollapseAfter == 0 || widget.CollapseAfter < -1 {
		widget.CollapseAfter = 7
	}

	// A bit cheeky, but from a user's perspective it makes more sense when channels and
	// playlists are separate things rather than specifying a list of channels and some of
	// them awkwardly have a "playlist:" prefix
	if len(widget.Playlists) > 0 {
		initialLen := len(widget.Channels)
		widget.Channels = append(widget.Channels, make([]string, len(widget.Playlists))...)

		for i := range widget.Playlists {
			widget.Channels[initialLen+i] = videosWidgetPlaylistPrefix + widget.Playlists[i]
		}
	}

	// Mark as first load and set ContentAvailable to false initially
	widget.isFirstLoad = true
	widget.ContentAvailable = false

	return nil
}

// update handles the widget update cycle with progressive caching
func (widget *videosWidget) update(ctx context.Context) {
	// Always fetch videos, but adjust cache duration based on load state
	if widget.isFirstLoad && !widget.ContentAvailable {
		slog.Info("Video widget first load - fetching videos with short cache")
		widget.withCacheDuration(3 * time.Second)
		widget.isFirstLoad = false
	}

	// Normal update flow - fetch videos
	widget.fetchVideos()
	
	// After successful fetch, extend cache duration for better performance
	if widget.ContentAvailable {
		widget.withCacheDuration(30 * time.Minute)
		slog.Info("Videos fetched successfully - extending cache duration")
	}
}

// fetchVideos fetches videos from both YouTube and Rumble sources
func (widget *videosWidget) fetchVideos() {
	slog.Info("Video widget update", "channels", widget.Channels, "rumble_channels", widget.RumbleChannels)

	// Fetch YouTube videos
	var allVideos videoList
	if len(widget.Channels) > 0 {
		youtubeVideos, err := fetchYoutubeChannelUploads(widget.Channels, widget.VideoUrlTemplate, widget.IncludeShorts)
		if err != nil {
			slog.Error("Failed to fetch YouTube videos", "error", err)
		} else {
			slog.Info("Successfully fetched YouTube videos", "count", len(youtubeVideos))
			allVideos = append(allVideos, youtubeVideos...)
		}
	}

	// Fetch Rumble videos
	if len(widget.RumbleChannels) > 0 {
		rumbleVideos, err := fetchRumbleChannelUploads(widget.RumbleChannels, widget.VideoUrlTemplate)
		if err != nil {
			slog.Error("Failed to fetch Rumble videos", "error", err)
		} else {
			slog.Info("Successfully fetched Rumble videos", "count", len(rumbleVideos))
			// Convert rumbleVideoList to videoList
			for _, rv := range rumbleVideos {
				allVideos = append(allVideos, video{
					ThumbnailUrl: rv.ThumbnailUrl,
					Title:        rv.Title,
					Url:          rv.Url,
					Author:       rv.Author,
					AuthorUrl:    rv.AuthorUrl,
					TimePosted:   rv.TimePosted,
				})
			}
		}
	}

	// Sort all videos by newest
	allVideos.sortByNewest()

	// Apply limit
	if len(allVideos) > widget.Limit {
		allVideos = allVideos[:widget.Limit]
	}

	slog.Info("Video widget update complete", "total_videos", len(allVideos))
	
	// Debug: Log first few videos to see what data we have
	for i, v := range allVideos {
		if i >= 3 { // Only log first 3 videos
			break
		}
		slog.Info("Video data", "index", i, "title", v.Title, "author", v.Author, "thumbnail", v.ThumbnailUrl, "url", v.Url, "time", v.TimePosted)
	}
	
	widget.Videos = allVideos
	widget.ContentAvailable = true
	slog.Info("Video content now available", "video_count", len(allVideos))
}

// Render generates the HTML output for the videos widget
func (widget *videosWidget) Render() template.HTML {
	var tmpl *template.Template

	slog.Info("Rendering video widget", "style", widget.Style, "video_count", len(widget.Videos), "content_available", widget.ContentAvailable)

	// If content is not available yet, show loading message with auto-refresh
	if !widget.ContentAvailable {
		slog.Info("Rendering loading state for videos")
		return template.HTML("<div class=\"widget-loading\">Loading videos...<script>setTimeout(function(){window.location.reload();}, 5000);</script></div>")
	}

	switch widget.Style {
	case "grid-cards":
		tmpl = videosWidgetGridTemplate
		slog.Info("Using grid template")
	case "vertical-list":
		tmpl = videosWidgetVerticalListTemplate
		slog.Info("Using vertical list template")
	default:
		tmpl = videosWidgetTemplate
		slog.Info("Using default template")
	}

	return widget.renderTemplate(widget, tmpl)
}

// =============================================================================
// VIDEO LIST METHODS
// =============================================================================

// sortByNewest sorts the video list by newest first
func (v videoList) sortByNewest() videoList {
	sort.Slice(v, func(i, j int) bool {
		return v[i].TimePosted.After(v[j].TimePosted)
	})

	return v
}

// sortByNewest sorts the rumble video list by newest first
func (v rumbleVideoList) sortByNewest() rumbleVideoList {
	sort.Slice(v, func(i, j int) bool {
		return v[i].TimePosted.After(v[j].TimePosted)
	})

	return v
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// parseYoutubeFeedTime parses YouTube feed time format
func parseYoutubeFeedTime(t string) time.Time {
	parsedTime, err := time.Parse("2006-01-02T15:04:05-07:00", t)
	if err != nil {
		return time.Now()
	}

	return parsedTime
}

// parseRumbleFeedTime parses Rumble feed time format
func parseRumbleFeedTime(t string) time.Time {
	// Handle invalid date strings
	if t == "" || t == "Invalid Date" {
		return time.Now()
	}
	
	parsedTime, err := time.Parse("Mon, 02 Jan 2006 15:04:05 GMT", t)
	if err != nil {
		// Try alternative formats
		parsedTime, err = time.Parse("Mon, 2 Jan 2006 15:04:05 GMT", t)
		if err != nil {
			return time.Now()
		}
	}

	return parsedTime
}

// =============================================================================
// API FETCHING FUNCTIONS
// =============================================================================

// fetchYoutubeChannelUploads fetches videos from YouTube channels/playlists
func fetchYoutubeChannelUploads(channelOrPlaylistIDs []string, videoUrlTemplate string, includeShorts bool) (videoList, error) {
	requests := make([]*http.Request, 0, len(channelOrPlaylistIDs))

	for i := range channelOrPlaylistIDs {
		var feedUrl string
		if strings.HasPrefix(channelOrPlaylistIDs[i], videosWidgetPlaylistPrefix) {
			feedUrl = "https://www.youtube.com/feeds/videos.xml?playlist_id=" +
				strings.TrimPrefix(channelOrPlaylistIDs[i], videosWidgetPlaylistPrefix)
		} else if !includeShorts && strings.HasPrefix(channelOrPlaylistIDs[i], "UC") {
			playlistId := strings.Replace(channelOrPlaylistIDs[i], "UC", "UULF", 1)
			feedUrl = "https://www.youtube.com/feeds/videos.xml?playlist_id=" + playlistId
		} else {
			feedUrl = "https://www.youtube.com/feeds/videos.xml?channel_id=" + channelOrPlaylistIDs[i]
		}

		request, _ := http.NewRequest("GET", feedUrl, nil)
		requests = append(requests, request)
	}

	job := newJob(decodeXmlFromRequestTask[youtubeFeedResponseXml](defaultHTTPClient), requests).withWorkers(30)
	responses, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	videos := make(videoList, 0, len(channelOrPlaylistIDs)*15)
	var failed int

	for i := range responses {
		if errs[i] != nil {
			failed++
			slog.Error("Failed to fetch youtube feed", "channel", channelOrPlaylistIDs[i], "error", errs[i])
			continue
		}

		response := responses[i]

		for j := range response.Videos {
			v := &response.Videos[j]
			var videoUrl string

			if videoUrlTemplate == "" {
				videoUrl = v.Link.Href
			} else {
				parsedUrl, err := url.Parse(v.Link.Href)

				if err == nil {
					videoUrl = strings.ReplaceAll(videoUrlTemplate, "{VIDEO-ID}", parsedUrl.Query().Get("v"))
				} else {
					videoUrl = "#"
				}
			}

			thumbnailUrl := v.Group.Thumbnail.Url
			if thumbnailUrl == "" {
				thumbnailUrl = "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='9'%3E%3Crect width='16' height='9' fill='%23ccc'/%3E%3C/svg%3E"
			}

			videos = append(videos, video{
				ThumbnailUrl: thumbnailUrl,
				Title:        v.Title,
				Url:          videoUrl,
				Author:       response.Channel,
				AuthorUrl:    response.ChannelLink + "/videos",
				TimePosted:   parseYoutubeFeedTime(v.Published),
			})
		}
	}

	if len(videos) == 0 {
		return nil, errNoContent
	}

	videos.sortByNewest()

	if failed > 0 {
		return videos, fmt.Errorf("%w: missing videos from %d channels", errPartialContent, failed)
	}

	return videos, nil
}

// fetchRumbleChannelUploads fetches videos from Rumble channels
func fetchRumbleChannelUploads(channelNames []string, videoUrlTemplate string) (rumbleVideoList, error) {
	requests := make([]*http.Request, 0, len(channelNames))

	for i := range channelNames {
		feedUrl := "http://rumble-rss.xyz/rumble/" + channelNames[i]
		request, _ := http.NewRequest("GET", feedUrl, nil)
		requests = append(requests, request)
	}

	job := newJob(decodeXmlFromRequestTask[rumbleFeedResponseXml](defaultHTTPClient), requests).withWorkers(30)
	responses, errs, err := workerPoolDo(job)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errNoContent, err)
	}

	videos := make(rumbleVideoList, 0, len(channelNames)*15)
	var failed int

	for i := range responses {
		if errs[i] != nil {
			failed++
			slog.Error("Failed to fetch rumble feed", "channel", channelNames[i], "error", errs[i])
			continue
		}

		response := responses[i]

		for j := range response.Videos {
			v := &response.Videos[j]
			
			// Skip videos with empty titles or links
			if v.Title == "" || v.Link == "" {
				continue
			}
			
			var videoUrl string

			if videoUrlTemplate == "" {
				videoUrl = v.Link
			} else {
				// For Rumble, we might want to extract video ID from the URL
				videoUrl = v.Link
			}

			// Use MediaThumbnail if available, otherwise use iTunes image
			thumbnailUrl := v.MediaThumbnail.Url
			if thumbnailUrl == "" {
				thumbnailUrl = v.Thumbnail.Url
			}
			if thumbnailUrl == "" {
				thumbnailUrl = "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='9'%3E%3Crect width='16' height='9' fill='%23ccc'/%3E%3C/svg%3E"
			}

			videos = append(videos, rumbleVideo{
				ThumbnailUrl: thumbnailUrl,
				Title:        v.Title,
				Url:          videoUrl,
				Author:       response.Channel,
				AuthorUrl:    response.ChannelLink,
				TimePosted:   parseRumbleFeedTime(v.Published),
			})
		}
	}

	if len(videos) == 0 {
		return nil, errNoContent
	}

	videos.sortByNewest()

	if failed > 0 {
		return videos, fmt.Errorf("%w: missing videos from %d channels", errPartialContent, failed)
	}

	return videos, nil
}
