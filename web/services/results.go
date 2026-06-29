package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/pkg/webpresence"
	"github.com/lib/pq"
)

type ResultsService struct {
	db  *sql.DB
	log *slog.Logger
}

func NewResultsService(db *sql.DB, logger *slog.Logger) *ResultsService {
	return &ResultsService{
		db:  db,
		log: logger.With(slog.String("service", "results")),
	}
}

// NullableJSON helps scan JSONB/text fields that may be NULL
type NullableJSON struct {
	Data  interface{}
	Valid bool
}

func (nj *NullableJSON) Scan(value interface{}) error {
	if value == nil {
		nj.Data = nil
		nj.Valid = false
		return nil
	}
	switch v := value.(type) {
	case []byte:
		nj.Valid = true
		if len(v) == 0 {
			nj.Data = nil
			return nil
		}
		var any interface{}
		if err := json.Unmarshal(v, &any); err != nil {
			// store as string if not valid JSON
			nj.Data = string(v)
		} else {
			nj.Data = any
		}
		return nil
	case string:
		nj.Valid = true
		if v == "" {
			nj.Data = nil
			return nil
		}
		var any interface{}
		if err := json.Unmarshal([]byte(v), &any); err != nil {
			nj.Data = v
		} else {
			nj.Data = any
		}
		return nil
	default:
		return fmt.Errorf("cannot scan %T into NullableJSON", value)
	}
}

func convertToInt(s string) (int, error) {
	if i, err := strconv.Atoi(s); err == nil {
		return i, nil
	}
	return 0, fmt.Errorf("invalid int string: %s", s)
}

func (s *ResultsService) GetJobResults(ctx context.Context, jobID string) ([]models.Result, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	const q = `SELECT
            id, user_id, job_id, input_id, link, cid, title,
            categories, category, address, website, phone, pluscode,
            review_count, rating, latitude, longitude, status_info,
            description, reviews_link, thumbnail, timezone, price_range,
            data_id, emails, created_at
        FROM results
        WHERE job_id = $1
        ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, q, jobID)
	if err != nil {
		s.log.Error("job_results_query_failed", slog.String("job_id", jobID), slog.Any("error", err))
		return nil, fmt.Errorf("failed to query results: %w", err)
	}
	defer rows.Close()
	var results []models.Result
	for rows.Next() {
		var r models.Result
		if err := rows.Scan(
			&r.ID, &r.UserID, &r.JobID, &r.InputID, &r.Link, &r.Cid, &r.Title,
			&r.Categories, &r.Category, &r.Address, &r.Website, &r.Phone, &r.PlusCode,
			&r.ReviewCount, &r.Rating, &r.Latitude, &r.Longitude, &r.Status,
			&r.Description, &r.ReviewsLink, &r.Thumbnail, &r.Timezone, &r.PriceRange,
			&r.DataID, &r.Emails, &r.CreatedAt,
		); err != nil {
			s.log.Error("job_results_scan_failed", slog.String("job_id", jobID), slog.Any("error", err))
			return nil, fmt.Errorf("failed to scan result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}
	s.log.Debug("job_results_retrieved", slog.String("job_id", jobID), slog.Int("count", len(results)))
	return results, nil
}

func (s *ResultsService) GetUserResults(ctx context.Context, userID string, limit, offset int) ([]models.Result, error) {
	if s.db == nil {
		return nil, fmt.Errorf("database not available")
	}
	const q = `SELECT
            id, user_id, job_id, input_id, link, cid, title,
            categories, category, address, website, phone, pluscode,
            review_count, rating, latitude, longitude, status_info,
            description, reviews_link, thumbnail, timezone, price_range,
            data_id, emails, created_at
        FROM results
        WHERE user_id = $1
        ORDER BY created_at DESC
        LIMIT $2 OFFSET $3`
	rows, err := s.db.QueryContext(ctx, q, userID, limit, offset)
	if err != nil {
		s.log.Error("user_results_query_failed", slog.String("user_id", userID), slog.Int("limit", limit), slog.Int("offset", offset), slog.Any("error", err))
		return nil, fmt.Errorf("failed to query results: %w", err)
	}
	defer rows.Close()
	var results []models.Result
	for rows.Next() {
		var r models.Result
		if err := rows.Scan(
			&r.ID, &r.UserID, &r.JobID, &r.InputID, &r.Link, &r.Cid, &r.Title,
			&r.Categories, &r.Category, &r.Address, &r.Website, &r.Phone, &r.PlusCode,
			&r.ReviewCount, &r.Rating, &r.Latitude, &r.Longitude, &r.Status,
			&r.Description, &r.ReviewsLink, &r.Thumbnail, &r.Timezone, &r.PriceRange,
			&r.DataID, &r.Emails, &r.CreatedAt,
		); err != nil {
			s.log.Error("user_results_scan_failed", slog.String("user_id", userID), slog.Any("error", err))
			return nil, fmt.Errorf("failed to scan result: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}
	s.log.Debug("user_results_retrieved", slog.String("user_id", userID), slog.Int("count", len(results)))
	return results, nil
}

// GetEnhancedJobResultsPaginated returns one page of a job's enhanced result
// rows, scoped to the requesting user and optionally filtered by web-presence
// tier. It scans (id, website) for the whole job, then classifies, counts,
// filters and paginates in Go (a job is capped at max_results, so the scan
// stays small), then fetches full-fat data for only the page's IDs. Both the
// scan and the fetch carry `user_id = $2` for tenant isolation. The returned
// counts are the UNFILTERED per-tier breakdown for the whole job (used for the
// "X of Y" UI summary).
func (s *ResultsService) GetEnhancedJobResultsPaginated(ctx context.Context, jobID, userID string, tiers []string, limit, offset int) ([]models.EnhancedResult, int, map[string]int, error) {
	if s.db == nil {
		return nil, 0, nil, fmt.Errorf("database not available")
	}

	// 1. Cheap scan of (id, website) for the whole job, in display order.
	//    A job is capped at max_results (<=500), so this stays small.
	const scanQ = `SELECT id, COALESCE(website, '')
        FROM results
        WHERE job_id = $1 AND user_id = $2
        ORDER BY created_at DESC`
	scanRows, err := s.db.QueryContext(ctx, scanQ, jobID, userID)
	if err != nil {
		s.log.Error("enhanced_results_scan_failed", slog.String("job_id", jobID), slog.String("user_id", userID), slog.Any("error", err))
		return nil, 0, nil, fmt.Errorf("failed to scan results: %w", err)
	}
	var rows []webpresence.Row
	for scanRows.Next() {
		var r webpresence.Row
		if err := scanRows.Scan(&r.ID, &r.Website); err != nil {
			scanRows.Close()
			return nil, 0, nil, fmt.Errorf("failed to scan result id/website: %w", err)
		}
		rows = append(rows, r)
	}
	if err := scanRows.Err(); err != nil {
		scanRows.Close()
		return nil, 0, nil, fmt.Errorf("row iteration error: %w", err)
	}
	scanRows.Close()

	// 2. Classify + count + filter + slice the page, all in Go.
	page := webpresence.Paginate(rows, tiers, limit, offset)
	if len(page.PageIDs) == 0 {
		return []models.EnhancedResult{}, page.Total, page.Counts, nil
	}

	// 3. Fetch full-fat rows for just this page's IDs. user_id keeps tenant
	//    isolation even though IDs already came from a user-scoped scan.
	const q = `SELECT
            id,
            COALESCE(user_id, '') as user_id,
            job_id::text as job_id,
            COALESCE(input_id, '') as input_id,
            COALESCE(link, '') as link,
            COALESCE(cid, '') as cid,
            title,
            COALESCE(categories, '') as categories,
            category,
            address,
            COALESCE(openhours, '{}') as openhours,
            COALESCE(popular_times, '{}') as popular_times,
            website,
            phone,
            pluscode,
            review_count,
            rating,
            COALESCE(reviews_per_rating, '{}') as reviews_per_rating,
            COALESCE(latitude, 0) as latitude,
            COALESCE(longitude, 0) as longitude,
            COALESCE(status_info, '') as status_info,
            COALESCE(description, '') as description,
            COALESCE(reviews_link, '') as reviews_link,
            COALESCE(thumbnail, '') as thumbnail,
            COALESCE(timezone, '') as timezone,
            COALESCE(price_range, '') as price_range,
            COALESCE(data_id, '') as data_id,
            COALESCE(images, '[]') as images,
            COALESCE(reservations, '[]') as reservations,
            COALESCE(order_online, '[]') as order_online,
            COALESCE(menu, '{}') as menu,
            COALESCE(owner, '{}') as owner,
            COALESCE(complete_address, '{}') as complete_address,
            COALESCE(about, '[]') as about,
            COALESCE(user_reviews, '[]') as user_reviews,
            COALESCE(emails, '') as emails,
            COALESCE(created_at, NOW()) as created_at
        FROM results
        WHERE id = ANY($1) AND user_id = $2`

	dataRows, err := s.db.QueryContext(ctx, q, pq.Array(page.PageIDs), userID)
	if err != nil {
		s.log.Error("enhanced_results_query_failed", slog.String("job_id", jobID), slog.String("user_id", userID), slog.Any("error", err))
		return nil, 0, nil, fmt.Errorf("failed to query enhanced results: %w", err)
	}
	defer dataRows.Close()

	byID := make(map[int]models.EnhancedResult, len(page.PageIDs))
	for dataRows.Next() {
		var r models.EnhancedResult
		var openHours, popularTimes, reviewsPerRating, menu, owner, completeAddress NullableJSON
		var images, reservations, orderOnline, about, userReviews NullableJSON
		if err := dataRows.Scan(
			&r.ID, &r.UserID, &r.JobID, &r.InputID, &r.Link, &r.Cid, &r.Title,
			&r.Categories, &r.Category, &r.Address,
			&openHours, &popularTimes,
			&r.Website, &r.Phone, &r.PlusCode,
			&r.ReviewCount, &r.Rating, &reviewsPerRating,
			&r.Latitude, &r.Longitude, &r.Status,
			&r.Description, &r.ReviewsLink, &r.Thumbnail, &r.Timezone, &r.PriceRange,
			&r.DataID,
			&images, &reservations, &orderOnline, &menu, &owner, &completeAddress,
			&about, &userReviews,
			&r.Emails, &r.CreatedAt,
		); err != nil {
			return nil, 0, nil, fmt.Errorf("failed to scan enhanced result: %w", err)
		}

		if openHours.Valid && openHours.Data != nil {
			switch data := openHours.Data.(type) {
			case string:
				if data != "" && data != "{}" {
					r.OpenHours = map[string][]string{"default": {data}}
				}
			case map[string]interface{}:
				r.OpenHours = make(map[string][]string)
				for day, times := range data {
					if timeSlice, ok := times.([]interface{}); ok {
						r.OpenHours[day] = make([]string, len(timeSlice))
						for i, t := range timeSlice {
							if timeStr, ok := t.(string); ok {
								r.OpenHours[day][i] = timeStr
							}
						}
					} else if timeStr, ok := times.(string); ok {
						r.OpenHours[day] = []string{timeStr}
					}
				}
			}
		}

		if popularTimes.Valid && popularTimes.Data != nil {
			if times, ok := popularTimes.Data.(map[string]interface{}); ok {
				r.PopularTimes = make(map[string]map[int]int)
				for day, dayTimes := range times {
					if dayTimesMap, ok := dayTimes.(map[string]interface{}); ok {
						r.PopularTimes[day] = make(map[int]int)
						for hour, traffic := range dayTimesMap {
							if hourInt, err := convertToInt(hour); err == nil {
								if trafficFloat, ok := traffic.(float64); ok {
									r.PopularTimes[day][hourInt] = int(trafficFloat)
								}
							}
						}
					}
				}
			}
		}

		if reviewsPerRating.Valid && reviewsPerRating.Data != nil {
			if ratings, ok := reviewsPerRating.Data.(map[string]interface{}); ok {
				r.ReviewsPerRating = make(map[int]int)
				for rating, count := range ratings {
					if ratingInt, err := convertToInt(rating); err == nil {
						if countFloat, ok := count.(float64); ok {
							r.ReviewsPerRating[ratingInt] = int(countFloat)
						}
					}
				}
			}
		}

		if images.Valid && images.Data != nil {
			if imageSlice, ok := images.Data.([]interface{}); ok {
				r.Images = make([]map[string]interface{}, len(imageSlice))
				for i, img := range imageSlice {
					if imgMap, ok := img.(map[string]interface{}); ok {
						r.Images[i] = imgMap
					}
				}
			}
		}

		if reservations.Valid && reservations.Data != nil {
			if resSlice, ok := reservations.Data.([]interface{}); ok {
				r.Reservations = make([]map[string]interface{}, len(resSlice))
				for i, res := range resSlice {
					if resMap, ok := res.(map[string]interface{}); ok {
						r.Reservations[i] = resMap
					}
				}
			}
		}

		if orderOnline.Valid && orderOnline.Data != nil {
			if orderSlice, ok := orderOnline.Data.([]interface{}); ok {
				r.OrderOnline = make([]map[string]interface{}, len(orderSlice))
				for i, order := range orderSlice {
					if orderMap, ok := order.(map[string]interface{}); ok {
						r.OrderOnline[i] = orderMap
					}
				}
			}
		}

		if about.Valid && about.Data != nil {
			if aboutSlice, ok := about.Data.([]interface{}); ok {
				r.About = make([]map[string]interface{}, len(aboutSlice))
				for i, ab := range aboutSlice {
					if abMap, ok := ab.(map[string]interface{}); ok {
						r.About[i] = abMap
					}
				}
			}
		}

		if userReviews.Valid && userReviews.Data != nil {
			if reviewSlice, ok := userReviews.Data.([]interface{}); ok {
				r.UserReviews = make([]map[string]interface{}, len(reviewSlice))
				for i, review := range reviewSlice {
					if reviewMap, ok := review.(map[string]interface{}); ok {
						r.UserReviews[i] = reviewMap
					}
				}
			}
		}

		if menu.Valid && menu.Data != nil {
			if menuMap, ok := menu.Data.(map[string]interface{}); ok {
				r.Menu = menuMap
			}
		}
		if owner.Valid && owner.Data != nil {
			if ownerMap, ok := owner.Data.(map[string]interface{}); ok {
				r.Owner = ownerMap
			}
		}
		if completeAddress.Valid && completeAddress.Data != nil {
			if addrMap, ok := completeAddress.Data.(map[string]interface{}); ok {
				r.CompleteAddress = addrMap
			}
		}

		r.WebPresence = string(webpresence.Classify(r.Website))
		byID[r.ID] = r
	}
	if err := dataRows.Err(); err != nil {
		return nil, 0, nil, fmt.Errorf("row iteration error: %w", err)
	}

	// Reassemble in page order (ANY() does not guarantee order).
	results := make([]models.EnhancedResult, 0, len(page.PageIDs))
	for _, id := range page.PageIDs {
		if r, ok := byID[id]; ok {
			results = append(results, r)
		}
	}

	s.log.Debug("enhanced_results_retrieved",
		slog.String("job_id", jobID), slog.Int("count", len(results)),
		slog.Int("total", page.Total), slog.Int("limit", limit), slog.Int("offset", offset))
	return results, page.Total, page.Counts, nil
}
