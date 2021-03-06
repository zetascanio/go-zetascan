package zetascan

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// Api struct for key, URL and method
type Api struct {
	apiKey      string
	apiURL      string
	ApiMethod   string
	apiVersion  string
	apiProtocol string
	DnsMethod   string
	DnsType     string
}

type Query struct {
	apiKey   string
	apiQuery string
}

// Format for JSON and JSONX responses

type JsonReason struct {
	Class       string `json:"class"`
	Rule        string `json:"rule"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Source      string `json:"source"`
	Port        string `json:"port"`
	SourcePort  string `json:"sourceport"`
	Destination string `json:"destination"`
}

type JsonExtended struct {
	ASNum   string     `json:"ASNum"`
	Route   string     `json:"route"`
	Country string     `json:"country"`
	Domain  string     `json:"domain"`
	State   string     `json:"state"`
	Time    string     `json:"time"`
	Reason  JsonReason `json:"reason"`
}

type JsonResults []struct {
	Item       string       `json:"item"`
	Found      bool         `json:"found"`
	Score      float64      `json:"score"`
	WebScore   float64      `json:"webscore"`
	FromSubnet bool         `json:"fromSubnet"`
	Sources    []string     `json:"sources"`
	Wl         bool         `json:"wl"`
	Wldata     string       `json:"wldata"`
	Extended   JsonExtended `json:"extended"`
}

type JsonRecord struct {
	Results       JsonResults `json:"results"`
	ExecutionTime int64       `json:"executionTime"`
	Status        string      `json:"status"`
}

type Results struct {
	IP          string
	Match       bool
	Expected    bool
	TimeElapsed int64
	Length      int
}

// Init specify an authentication key for authentication
func (myapi Api) Init(apiKey string, ipcheck bool) (myapi2 Api, err error) {

	if apiKey != "" {
		myapi.apiKey = apiKey
		//return myapi, errors.New("API Key must be specified")
	}

	// TODO: Change to new zetascan URL
	// a.	restlb.zetascan.com – load balanced and high availability end point – a bit slower, but guarantees 100% response rate.
	// b.	api.zetascan.com – a bit faster. However, you may hit a server with high load or currently not functioning. You should handle this and issue another request.
	// c.	dnslb.zetasca.com – experimental DNS Load balancer and high availability end point.

	myapi.apiURL = "api.zetascan.com"
	myapi.apiProtocol = myapi.ToggleSSL(true) // Default to SSL
	myapi.ApiMethod = "http"

	// Version bump from v1 to v2 for Zetascan v2 release
	myapi.apiVersion = "v2"

	// DNS has two methods, direct or
	myapi.DnsMethod = "nameserver"

	// Support lookups with A records or txt
	myapi.DnsType = "A"

	// Check if https required
	if myapi.apiProtocol == "http" && apiKey != "" && ipcheck == false {
		return myapi, errors.New("https required if using API key without ip check")
	}

	return myapi, nil
}

// Query a domain/IP via any method (text, html, json, jsonx, dns)
func (myapi Api) Query(query string) (m JsonRecord, err error) {

	// If DNS, run a specific function, otherwise all web queries via http.Get
	if myapi.ApiMethod == "dns" {
		results, _ := myapi.QueryDNS(query, 3)
		m, _ = myapi.ParseDNS(results)

	} else {
		res, err := http.Get(myapi.getUrl(query))

		// URL malformed? Return an error
		if res.StatusCode == 404 {
			return m, errors.New("Invalid request, check URL not malformed: " + myapi.getUrl(query))
		}

		// Forbidden? Return an error
		if res.StatusCode == 403 {
			return m, errors.New("Request forbidden, check API key or IP for authorization: " + myapi.getUrl(query))
		}

		//fmt.Println(myapi.getUrl(query), res, err)

		if err != nil {
			return m, err
		}

		m, err = myapi.parseResult(res)

		//fmt.Println(err)

		if err != nil {
			return m, err
		}

	}

	return m, nil

}

// Verify a query to zetascan is returning valid data
func (myapi Api) Verify(status bool, verbose bool) (totalResults []Results, err error) {

	tests := make(map[string]bool)

	// Records that will pass (whitelist)
	tests["okdomain.org"] = false
	tests["127.9.9.4"] = false

	// Records that will fail (blacklisted)
	tests["baddomain.org"] = true
	tests["127.9.9.1"] = true
	tests["127.9.9.2"] = true
	tests["127.9.9.3"] = true

	//for i := 0; i < len(tests); i++ {
	for key, value := range tests {

		if verbose == true {
			fmt.Println("Testing", key, value)
		}

		// Time the query length
		startTime := time.Now()

		// Fetch the result
		response, err := myapi.Query(key)

		m := time.Duration(time.Since(startTime))
		durationTime := int64(m / time.Millisecond)

		if verbose == true {
			fmt.Println("Response =>", response)
		}

		if err != nil {
			fmt.Println(err)
		}

		// Does it match?
		match := myapi.IsMatch(&response)

		/*
			if match == true && value != true {
				fmt.Println(key, ": Failed (", durationTime, ")")
			}

			if match == true {
				fmt.Println(key, ": Matched (", durationTime, ")")
			} else {
				fmt.Println(key, ": No hit (", durationTime, ")")
			}

			if verbose == true {
				fmt.Println("Resp => ", res, "\n")
			}
		*/

		// Store the results and return the group in a struct, regardless of the method
		result := Results{
			IP:          key,
			TimeElapsed: durationTime,
			Match:       match,
			Expected:    value,
		}

		// Append each result
		totalResults = append(totalResults, result)

	}

	// Return all matches
	return totalResults, nil
}

// getUrl Return a URL to query zetascan
func (myapi Api) getUrl(domain string) string {

	// Encode the apiKey if specified
	v := url.Values{}

	// If the API key is specified, add the query URI
	if myapi.apiKey != "" {
		v.Set("key", myapi.apiKey)
	}

	// TODO: Improve
	str := myapi.apiProtocol + "://" + myapi.apiURL + "/" + myapi.apiVersion + "/check/" + myapi.ApiMethod + "/" + domain + "?" + v.Encode()

	return str
}

// parseResult returns a struct with the zetascan response, regardless of the query method
func (myapi Api) parseResult(resp *http.Response) (data JsonRecord, err error) {

	// Init our object (Results is a []struct must be manually created)
	data = JsonRecord{
		Results: []struct {
			Item       string       `json:"item"`
			Found      bool         `json:"found"`
			Score      float64      `json:"score"`
			WebScore   float64      `json:"webscore"`
			FromSubnet bool         `json:"fromSubnet"`
			Sources    []string     `json:"sources"`
			Wl         bool         `json:"wl"`
			Wldata     string       `json:"wldata"`
			Extended   JsonExtended `json:"extended"`
		}{
			{},
		},
	}

	// Read the response
	body, err := ioutil.ReadAll(resp.Body)

	if err != nil {
		return data, err
	}
	// Choose which method use (http, text, json/jsonx)
	switch myapi.ApiMethod {

	case "http":
		{

			if resp.StatusCode == 204 {
				data.Results[0].Found = false
			} else {

				//data.Results[0].Found = true
			}

			/*
			   Sample header response:

			   Cache-Control:no-cache, no-store, must-revalidate
			   Connection:keep-alive
			   Content-Length:2
			   Content-Type:text/plain; charset=utf-8
			   Date:Mon, 02 Oct 2017 06:09:39 GMT
			   Expires:0
			   Pragma:no-cache
			   Server:nginx/1.13.1
			   Strict-Transport-Security:max-age=63072000; includeSubdomains
			   X-Content-Type-Options:nosniff

			   X-Frame-Options:DENY
			   x-zetascan-items:baddomain.org
			   -x-zetascan-score:1
			   -x-zetascan-sources:DBL;RED;GREY;GOLD;BLACK
			   x-zetascan-status:success
			   x-zetascan-time:1500970900
			   -x-zetascan-webscore:0.6
			   x-zetascan-wl:null
			*/

			// Populate our struct with details of the request
			data.Results[0].Score, _ = strconv.ParseFloat(resp.Header.Get("x-zetascan-score"), 32)
			data.Results[0].WebScore, _ = strconv.ParseFloat(resp.Header.Get("x-zetascan-webscore"), 32)

			// Populate our struct with details of the request
			data.Results[0].Sources = strings.Split(";", resp.Header.Get("x-zetascan-sources"))

			// TODO: Clarify, since JSON wl and wl-data differ from HTTP query
			//wl := resp.Header.Get("x-zetascan-wl")

			// TODO: Workaround. HTTP should return a wl header.
			if data.Results[0].Score <= -0.1 {
				data.Results[0].Wl = true
			} else {
				data.Results[0].Wl = false
			}

			// Todo, Split based on ; similar to Sources?
			data.Results[0].Wldata = resp.Header.Get("x-zetascan-wl")

			data.Status = resp.Header.Get("x-zetascan-success")

			// TODO: Workaround, since HTTP missing the found header
			if data.Results[0].Wl == true {
				data.Results[0].Found = false
			} else if data.Results[0].Score > 0 {
				data.Results[0].Found = true
			}

		}

	case "text":
		{

			// Read the body and split from the specified API formatting
			bodyString := string(body)
			head := strings.Split(bodyString, ":")
			str := strings.Split(head[1], ",")

			/*
				http://docs.zetascan.io/?php#http-format
				item:bool,bool,wldata,score,source

				Where:

				the first bool is true, if found in any black list,
				the second bool is true, if found in any white list,
				wldata contains the data from the white list, and
				score is followed by the list of sources where the item was found.

				Updated for v2

				baddomain.org:true,false,,1,0.6,dbl,red,gold,grey,black okdomain.org:true,true,,-0.1,-0.1,white 127.9.9.1:true,false,,0.95,0.6,xbl,sbl

				okdomain.org:false,true,,-0.1,-0.1,white

			*/

			// Are we included in a blacklist?
			if str[0] == "true" {
				data.Results[0].Found = true
			} else {
				data.Results[0].Found = false
			}

			// Are we included in a whitelist?
			if str[1] == "true" {
				data.Results[0].Wl = true
			} else {
				data.Results[0].Wl = false
			}

			data.Results[0].Wldata = str[2]

			// TODO: Should be a float32 vs float64
			data.Results[0].Score, _ = strconv.ParseFloat(str[3], 32)

			// TODO: Group together all sources into a response array
			if len(str) > 3 {
				//data.Results[0].Sources = str[4:len(str)]
			}

		}

	case "json", "jsonx":
		{

			/*
				http://docs.zetascan.io/?php#json-format

				Formatting of a JSON response:

				{
					"results": [{
					"item": "123.123.123.123",
					"found": true,
					"score": 0.2,
					"fromSubnet": true,
					"sources": ["shPBL"],
					"wl": false,
					"wldata": ""
					}],
					"executionTime": 2,
					"status": "success"
				}
			*/

			// Decode the JSON response into our defined struct
			dec := json.NewDecoder(strings.NewReader(string(body)))
			for {

				if err := dec.Decode(&data); err == io.EOF {
					return data, nil
				} else if err != nil {
					return data, err
				}

			}

		}

	}

	return data, nil

}

// TODO: getInfo returns a struct with expanded information on why the result listed
func (myapi Api) getInfo(resp *http.Response) (status bool, err error) {

	return true, nil

}

// isMatch return if a result matched a whitelist/blacklist
func (myapi Api) IsMatch(response *JsonRecord) (status bool) {

	// Is the record blacklisted?
	if response.Results[0].Found == true {
		return true
	}

	return false

}

// IsWhiteList return if a result matched a whitelist
func (myapi Api) IsWhiteList(response *JsonRecord) (status bool) {

	// Is the record a whitelist?
	if response.Results[0].Wl == true {
		return true
	}

	return false

}

// IsBlackList return if a result matched a blacklist
func (myapi Api) IsBlackList(response *JsonRecord) (status bool) {

	// Is the record a whitelist?
	if response.Results[0].Found == true && response.Results[0].Wl == false {
		return true
	}

	return false

}

// Return the score if a result matched a whitelist/blacklist on the MTA/default score
func (myapi Api) Score(response *JsonRecord) (score float64) {

	// Is the record a whitelist?
	if response.Results[0].Found == true || response.Results[0].Wl == true {
		return response.Results[0].Score
	}

	return

}

// Return the score if a result matched a whitelist/blacklist on the Webscore value
func (myapi Api) WebScore(response *JsonRecord) (score float64) {

	// Is the record a whitelist?
	if response.Results[0].Found == true || response.Results[0].Wl == true {
		return response.Results[0].WebScore
	}

	return

}

// Toggle SSL support
func (myapi Api) ToggleSSL(ssl bool) (str string) {

	if ssl == false {
		myapi.apiProtocol = "http"
	} else {
		myapi.apiProtocol = "https"
	}

	return myapi.apiProtocol

}

// Return the API key used
func (myapi Api) GetConf() string {

	return myapi.apiKey
}

// Preform a DNS query against the zetascan API
func (myapi Api) ParseDNS(results []net.IP) (data JsonRecord, err error) {

	// Move to a function to init?
	// Init our object (Results is a []struct must be manually created)
	data = JsonRecord{
		Results: []struct {
			Item       string       `json:"item"`
			Found      bool         `json:"found"`
			Score      float64      `json:"score"`
			WebScore   float64      `json:"webscore"`
			FromSubnet bool         `json:"fromSubnet"`
			Sources    []string     `json:"sources"`
			Wl         bool         `json:"wl"`
			Wldata     string       `json:"wldata"`
			Extended   JsonExtended `json:"extended"`
		}{
			{},
		},
	}

	// Parse the result from DNS and build the struct similar to http/text/json(x) methods

	// List through all matches, do we have a hit?
	for _, match := range results {

		// Firstly, do we have a blacklist hit?
		if strings.HasPrefix(match.String(), "127.8.0") == false && strings.HasPrefix(match.String(), "127.") {
			data.Results[0].Found = true
		}

		// Spamhaus
		if strings.HasPrefix(match.String(), "127.0.0") {
			//fmt.Println("Spamhaus hit")
		}

		// Spamhaus abuse
		if strings.HasPrefix(match.String(), "127.0.1") {
			//fmt.Println("Spamhaus abuse")
		}

		// URIBL match
		if strings.HasPrefix(match.String(), "127.1.0") {
			//fmt.Println("URIBL abuse")
		}

		// IP White lists from DNSWL
		if strings.HasPrefix(match.String(), "127.8.0") {
			//fmt.Println("DNSWL whitelist")
		}

	}

	return data, nil

}

// Preform a DNS query against the zetascan API
func (myapi Api) QueryDNS(query string, retry int) (json []net.IP, err error) {

	// Assemble our DNS query parts
	msg := new(dns.Msg)
	msg.Id = dns.Id()
	msg.RecursionDesired = true
	msg.Question = make([]dns.Question, 1)

	// Build the query
	msg.Question[0] = dns.Question{Name: dns.Fqdn(query), Qtype: dns.TypeA, Qclass: dns.ClassINET}

	// Use the zetascan DNS server directly for the query

	// TODO:
	// The new (v2) format allows only A, AAAA and TXT queries, and is as follows:domain.com.{key}.api.zetascan.com
	// Currenrtly using the v1 method
	// dig baddomain.org @api.zetascan.com

	in, err := dns.Exchange(msg, "api.zetascan.com:53")

	// Load the result(s) into a net.IP struct
	result := []net.IP{}

	// Timeout? Try again, max retry times
	if err != nil {

		// Failed, try again ...
		if strings.HasSuffix(err.Error(), "i/o timeout") && retry > 0 {
			retry--
			return myapi.QueryDNS(query, retry)
		}

		return nil, err

	}

	// Append all responses into an array
	for _, record := range in.Answer {
		if t, ok := record.(*dns.A); ok {
			result = append(result, t.A)
		}
	}

	return result, nil
}
