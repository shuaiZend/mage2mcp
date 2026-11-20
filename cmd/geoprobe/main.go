package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

func main() {
	from, _ := time.Parse("2006-01-02", "2026-11-19")
	to, _ := time.Parse("2006-01-02", "2026-11-20")
	query := url.Values{}
	query.Set("searchCriteria[pageSize]", strconv.Itoa(200))
	query.Set("searchCriteria[currentPage]", "1")
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "created_at")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", from.UTC().Format("2006-01-02 15:04:05"))
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "gteq")
	query.Set("searchCriteria[filter_groups][0][filters][1][field]", "created_at")
	query.Set("searchCriteria[filter_groups][0][filters][1][value]", to.UTC().Format("2006-01-02 15:04:05"))
	query.Set("searchCriteria[filter_groups][0][filters][1][condition_type]", "lt")
	query.Set("searchCriteria[sortOrders][0][field]", "created_at")
	query.Set("searchCriteria[sortOrders][0][direction]", "desc")
	target := "https://www.giztop.com/rest/default/V1/orders?" + query.Encode()
	fmt.Println("URL:", target)
	req, _ := http.NewRequest("GET", target, nil)
	req.Header.Set("Authorization", "Bearer "+os.Getenv("AK"))
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil { panic(err) }
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var d map[string]any
	_ = json.Unmarshal(raw, &d)
	fmt.Println("status", resp.StatusCode, "total_count", d["total_count"], "search_criteria", d["search_criteria"])
}
