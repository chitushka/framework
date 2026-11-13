package main

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/chitushka/framework/web"
)

type article struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

type articles struct {
	mu       sync.RWMutex
	nextID   int64
	capacity int
	values   map[int64]article
}

func newArticles(capacity int) *articles {
	return &articles{nextID: 2, capacity: capacity, values: map[int64]article{1: {ID: 1, Title: "Welcome to Chitu"}}}
}

type listOptions struct {
	limit, offset int
	title, sort   string
}
type page struct {
	Data []article `json:"data"`
	Meta struct {
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
		Total  int `json:"total"`
	} `json:"meta"`
}

func parseList(ctx *web.Context) (listOptions, error) {
	return listQuery(ctx.Request.URL.RawQuery)
}

func (s *articles) list(opts listOptions) page {
	s.mu.RLock()
	items := make([]article, 0, len(s.values))
	for _, item := range s.values {
		if strings.Contains(strings.ToLower(item.Title), strings.ToLower(opts.title)) {
			items = append(items, item)
		}
	}
	s.mu.RUnlock()
	descending := strings.HasPrefix(opts.sort, "-")
	byTitle := strings.TrimPrefix(opts.sort, "-") == "title"
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if descending {
			a, b = b, a
		}
		if byTitle && a.Title != b.Title {
			return a.Title < b.Title
		}
		return a.ID < b.ID
	})
	result := page{}
	result.Meta.Limit, result.Meta.Offset, result.Meta.Total = opts.limit, opts.offset, len(items)
	start := min(opts.offset, len(items))
	result.Data = items[start:min(start+opts.limit, len(items))]
	return result
}

func articleID(ctx *web.Context) (int64, error) {
	id, err := strconv.ParseInt(ctx.Param("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, web.NewHTTPError(404, "", nil)
	}
	return id, nil
}

func inputArticle(ctx *web.Context) (article, error) {
	var input struct {
		Title *string `json:"title"`
	}
	if err := ctx.BindJSON(&input); err != nil {
		return article{}, err
	}
	if input.Title == nil || !utf8.ValidString(*input.Title) {
		return article{}, web.NewHTTPError(422, "", nil)
	}
	title := strings.TrimSpace(*input.Title)
	if title == "" || len(title) > 200 {
		return article{}, web.NewHTTPError(422, "", nil)
	}
	return article{Title: title}, nil
}

func (s *articles) show(ctx *web.Context) error {
	id, err := articleID(ctx)
	if err != nil {
		return err
	}
	s.mu.RLock()
	item, found := s.values[id]
	s.mu.RUnlock()
	if !found {
		return web.NewHTTPError(404, "", nil)
	}
	return ctx.JSON(200, map[string]any{"data": item})
}

func (s *articles) create(ctx *web.Context) error {
	item, err := inputArticle(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if len(s.values) >= s.capacity || s.nextID == math.MaxInt64 {
		s.mu.Unlock()
		return web.NewHTTPError(503, "", nil)
	}
	item.ID = s.nextID
	s.nextID++
	s.values[item.ID] = item
	s.mu.Unlock()
	ctx.Writer.Header().Set("Location", "/v1/articles/"+strconv.FormatInt(item.ID, 10))
	return ctx.JSON(201, map[string]any{"data": item})
}

func (s *articles) update(ctx *web.Context) error {
	id, err := articleID(ctx)
	if err != nil {
		return err
	}
	item, err := inputArticle(ctx)
	if err != nil {
		return err
	}
	item.ID = id
	s.mu.Lock()
	_, found := s.values[id]
	if found {
		s.values[id] = item
	}
	s.mu.Unlock()
	if !found {
		return web.NewHTTPError(404, "", nil)
	}
	return ctx.JSON(200, map[string]any{"data": item})
}

func (s *articles) remove(ctx *web.Context) error {
	id, err := articleID(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	_, found := s.values[id]
	delete(s.values, id)
	s.mu.Unlock()
	if !found {
		return web.NewHTTPError(404, "", nil)
	}
	return ctx.NoContent(204)
}
