package main

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

func GetCatalogSyncData() (*CatalogSyncData, error) {
	if CatalogDB == nil {
		return nil, fmt.Errorf("catalog database unavailable")
	}
	tx, e := CatalogDB.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	data, e := catalogSnapshot(tx, time.Now())
	if e != nil {
		return nil, e
	}
	return data, tx.Commit()
}
func catalogSnapshot(tx *sql.Tx, now time.Time) (*CatalogSyncData, error) {
	data := &CatalogSyncData{Timestamp: now.UTC(), Shows: []ShowData{}, Movies: []MovieData{}, Recent: []RecentItem{}}
	rows, e := tx.Query(`SELECT media_type,COALESCE(show_name,''),COALESCE(season,''),COALESCE(episode,''),COALESCE(title,''),COALESCE(year,''),COALESCE(quality,''),filename,file_path,COALESCE(file_size,0),COALESCE(CAST(modified_at AS TEXT),''),COALESCE(CAST(added_to_catalog AS TEXT),'') FROM files WHERE status='active' ORDER BY added_to_catalog DESC,file_path`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	shows := map[string]map[string][]EpisodeData{}
	var tvSize, movieSize int64
	for rows.Next() {
		var kind, show, season, episode, title, year, quality, name, path, modified, added string
		var size int64
		if e = rows.Scan(&kind, &show, &season, &episode, &title, &year, &quality, &name, &path, &size, &modified, &added); e != nil {
			return nil, e
		}
		if kind != "tv" && kind != "movie" {
			return nil, fmt.Errorf("catalog contains unknown media type")
		}
		if kind == "tv" {
			if shows[show] == nil {
				shows[show] = map[string][]EpisodeData{}
			}
			shows[show][season] = append(shows[show][season], EpisodeData{episode, name, path, size, modified})
			data.Statistics.TotalEpisodes++
			tvSize += size
		} else {
			data.Movies = append(data.Movies, MovieData{title, year, quality, name, path, size, modified})
			movieSize += size
		}
		if len(data.Recent) < 50 {
			data.Recent = append(data.Recent, RecentItem{kind, show, season, episode, title, year, name, added})
		}
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	for name, seasons := range shows {
		s := ShowData{ShowName: name, Seasons: []SeasonData{}}
		for season, episodes := range seasons {
			sort.Slice(episodes, func(i, j int) bool {
				if episodes[i].Episode == episodes[j].Episode {
					return episodes[i].FilePath < episodes[j].FilePath
				}
				return episodes[i].Episode < episodes[j].Episode
			})
			s.Seasons = append(s.Seasons, SeasonData{season, episodes})
		}
		sort.Slice(s.Seasons, func(i, j int) bool { return s.Seasons[i].Season < s.Seasons[j].Season })
		data.Shows = append(data.Shows, s)
	}
	sort.Slice(data.Shows, func(i, j int) bool { return data.Shows[i].ShowName < data.Shows[j].ShowName })
	sort.Slice(data.Movies, func(i, j int) bool { return data.Movies[i].FilePath < data.Movies[j].FilePath })
	data.Statistics.TotalShows = len(data.Shows)
	data.Statistics.TotalMovies = len(data.Movies)
	data.Statistics.TotalSizeBytes = tvSize + movieSize
	data.Statistics.TotalSizeGB = float64(tvSize+movieSize) / (1 << 30)
	data.Statistics.TVSizeGB = float64(tvSize) / (1 << 30)
	data.Statistics.MoviesSizeGB = float64(movieSize) / (1 << 30)
	return data, nil
}
