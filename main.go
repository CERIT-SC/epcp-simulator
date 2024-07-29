package main

import (
	"bytes"
	"encoding/xml"
	e "errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
    _ "time/tzdata"
)

var (
	infoLogger     *log.Logger
	errorLogger    *log.Logger
	hoursInThePast time.Duration
	wsdlService    string
)

const scalingMaxFreqFile = "/sys/devices/system/cpu/cpu%d/cpufreq/scaling_max_freq"
const scalingAvailableFrequenciesFile = "/sys/devices/system/cpu/cpu0/cpufreq/scaling_available_frequencies"

type Times struct {
	startDate string
	endDate   string
	startHour string
	endHour   string
}

type ElectricityDailyForAgentureTrade struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName              xml.Name `xml:"Body"`
		GetDamPriceEResponse struct {
			XMLName xml.Name `xml:"http://www.ote-cr.cz/schema/service/public GetDamPriceEResponse"`
			Result  struct {
				XMLName xml.Name `xml:"Result"`
				Items   []struct {
					XMLName xml.Name `xml:"Item"`
					Date    string   `xml:"Date"`
					Hour    int      `xml:"Hour"`
					Price   float32  `xml:"Price"`
					Volume  float32  `xml:"Volume"`
				} `xml:"Item"`
			} `xml:"Result"`
		} `xml:"GetDamPriceEResponse"`
	} `xml:"Body"`
}

type ElectricityDayAheadTrade struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName              xml.Name `xml:"Body"`
		GetDamIndexEResponse struct {
			XMLName xml.Name `xml:"http://www.ote-cr.cz/schema/service/public GetDamIndexEResponse"`
			Result  struct {
				XMLName  xml.Name `xml:"Result"`
				DamIndex []struct {
					XMLName     xml.Name `xml:"DamIndex"`
					Date        string   `xml:"Date"`
					EurRate     float32  `xml:"EurRate"`
					BaseLoad    float32  `xml:"BaseLoad"`
					PeakLoad    float32  `xml:"PeakLoad"`
					OffpeakLoad float32  `xml:"OffpeakLoad"`
					Emerg       int      `xml:"Emerg""`
				} `xml:"DamIndex"`
			} `xml:"Result"`
		} `xml:"GetDamIndexEResponse"`
	} `xml:"Body"`
}

type ElectricityIntraDayTrade struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName             xml.Name `xml:"Body"`
		GetImPriceEResponse struct {
			XMLName xml.Name `xml:"http://www.ote-cr.cz/schema/service/public GetImPriceEResponse"`
			Result  struct {
				XMLName xml.Name `xml:"Result"`
				Item    []struct {
					XMLName xml.Name `xml:"Item"`
					Date    string   `xml:"Date"`
					Hour    int      `xml:"Hour"`
					Price   float32  `xml:"Price"`
					Volume  float32  `xml:"Volume"`
				} `xml:"Item"`
			} `xml:"Result"`
		} `xml:"GetImPriceEResponse"`
	} `xml:"Body"`
}

func init() {
	infoLogger = log.New(os.Stdout, "INFO: ", log.Ldate|log.Ltime|log.Lshortfile)
	errorLogger = log.New(os.Stderr, "ERROR: ", log.Ldate|log.Ltime|log.Lshortfile)
}

func sendRequest(soapAction string, payload []byte) *http.Response {
	req, err := http.NewRequest("POST", wsdlService, bytes.NewReader(payload))
	if err != nil {
		errorLogger.Printf("Error on creating request object: %s\n", err.Error())
		return nil
	}
	req.Header.Set("Content-type", "text/xml")
	req.Header.Set("SOAPAction", soapAction)
	client := &http.Client{}
	res, err := client.Do(req)
	if err != nil {
		errorLogger.Printf("Error on dispatching request: %s\n", err.Error())
		return nil
	}
	if res.Status != "200 OK" {
		errorLogger.Printf("Status %s on result: %s\n", res.Status, res)
		return nil
	}
	return res
}

func parseGetDamPriceE(res *http.Response) {
	result := new(ElectricityDailyForAgentureTrade)
	err := xml.NewDecoder(res.Body).Decode(result)
	if err != nil {
		errorLogger.Printf("Error on unmarshaling xml: %s\n", err.Error())
		return
	}
	hourlyRate := result.Body.GetDamPriceEResponse.Result.Items
	var i float32 = 0
	for _, s := range hourlyRate {
		infoLogger.Printf("Date: %s Hour: %d Price: %f Volume: %f\n", s.Date, s.Hour, s.Price, s.Volume)
		i += s.Price
	}
}

func parseGetDamIndexE(res *http.Response) {
	result := new(ElectricityDayAheadTrade)
	err := xml.NewDecoder(res.Body).Decode(result)
	if err != nil {
		infoLogger.Printf("Error on unmarshaling xml: %s\n", err.Error())
		return
	}
	loadIndex := result.Body.GetDamIndexEResponse.Result.DamIndex
	for _, index := range loadIndex {
		infoLogger.Printf("Date: %s BaseLoad: %f, PeakLoad: %f, OffPeakLoad: %f\n",
			index.Date, index.BaseLoad, index.PeakLoad, index.OffpeakLoad)
	}
}

func extractPricesFromGetImPriceE(res *http.Response) ([]float32, error) {
	var prices []float32
	result := new(ElectricityIntraDayTrade)
	err := xml.NewDecoder(res.Body).Decode(result)
	if err != nil {
		errorLogger.Printf("Error on unmarshaling xml: %s\n", err.Error())
		return prices, err
	}
	hourlyRate := result.Body.GetImPriceEResponse.Result.Item
	for _, s := range hourlyRate {
		infoLogger.Printf("Date: %s Hour: %d Price: %f Volume: %f\n", s.Date, s.Hour, s.Price, s.Volume)
		prices = append(prices, s.Price)
	}
	return prices, nil
}

// Vraci hodnotu energie a cenu v EUR po hodinách z denního trhu s elektřinou pro zadané období. (pro
// agentury)
// https://www.ote-cr.cz/cs/dokumentace/dokumentace-elektrina/uzivatelsky-manual_webove_sluzby_ote_c.pdf
//
// optional: startHour (int), EndHour (int), InEur (bool)
func getDamPriceE(startDate, endDate string) {
	payload := []byte(strings.TrimSpace(fmt.Sprintf(`
	<?xml version="1.0" encoding="UTF-8" ?>
    <soapenv:Envelope
       xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
       xmlns:pub="http://www.ote-cr.cz/schema/service/public">
		<soapenv:Header/>
        <soapenv:Body>
            <pub:GetDamPriceE>
				<pub:StartDate>%s</pub:StartDate>
				<pub:EndDate>%s</pub:EndDate>
				<!--<pub:StartHour>[int?]</pub:StartHour>-->
				<!--<pub:EndHour>[int?]</pub:EndHour>-->
				<!--<pub:InEur>[boolean?]</pub:InEur>-->
            </pub:GetDamPriceE>
        </soapenv:Body>
    </soapenv:Envelope>`, startDate, endDate),
	))
	soapAction := "urn:GetDamPriceE" // The format is `urn:<soap_action>`
	httpResponse := sendRequest(soapAction, payload)
	if httpResponse == nil {
		return
	}
	parseGetDamPriceE(httpResponse)
}

// GetDamIndexE Vraci indexy krátkodobého obchodu za elektřinu pro zadané období.
// https://www.ote-cr.cz/cs/dokumentace/dokumentace-elektrina/uzivatelsky-manual_webove_sluzby_ote_c.pdf
//
// neviem, ci to chapem spravne, ale vracia cenu za ktoru sa predala eletrina
// na base/peak/offpeak load na ten den - je to asi blokovy trh podla
// https://www.ote-cr.cz/cs/kratkodobe-trhy/elektrina/files-informace-vdt-vt/trh_s_elektrinou.pdf
func GetDamIndexE(startDate, endDate string) {
	payload := []byte(strings.TrimSpace(fmt.Sprintf(`
	<?xml version="1.0" encoding="UTF-8" ?>
    <soapenv:Envelope
       xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
       xmlns:pub="http://www.ote-cr.cz/schema/service/public">
		<soapenv:Header/>
        <soapenv:Body>
            <pub:GetDamIndexE>
				<pub:StartDate>%s</pub:StartDate>
				<pub:EndDate>%s</pub:EndDate>
            </pub:GetDamIndexE>
        </soapenv:Body>
    </soapenv:Envelope>`, startDate, endDate),
	))
	soapAction := "urn:GetDamIndexE" // The format is `urn:<soap_action>`
	httpResponse := sendRequest(soapAction, payload)
	if httpResponse == nil {
		return
	}
	parseGetDamIndexE(httpResponse)
}

// GetImPriceE Vraci ceny a množství za vnitrodenní obchody s elektřinou pro zadané období.
// https://www.ote-cr.cz/cs/dokumentace/dokumentace-elektrina/uzivatelsky-manual_webove_sluzby_ote_c.pdf
func GetImPriceE(startDate, endDate, startHour, endHour string) *http.Response {
	payload := []byte(strings.TrimSpace(fmt.Sprintf(`
	<?xml version="1.0" encoding="UTF-8" ?>
    <soapenv:Envelope
       xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"
       xmlns:pub="http://www.ote-cr.cz/schema/service/public">
		<soapenv:Header/>
        <soapenv:Body>
            <pub:GetImPriceE>
				<pub:StartDate>%s</pub:StartDate>
				<pub:EndDate>%s</pub:EndDate>
				<pub:StartHour>%s</pub:StartHour>
				<pub:EndHour>%s</pub:EndHour>
            </pub:GetImPriceE>
        </soapenv:Body>
    </soapenv:Envelope>`, startDate, endDate, startHour, endHour),
	))
	soapAction := "urn:GetImPriceE" // The format is `urn:<soap_action>`
	httpResponse := sendRequest(soapAction, payload)
	if httpResponse == nil {
		return nil
	}
	return httpResponse
}

// getTimeRange returns Times struct filled with start/end date/hour
func getTimeRange() *Times {
	times := new(Times)
	loc, err := time.LoadLocation("Europe/Budapest")
    if err != nil {
        errorLogger.Fatalf("Error getting location: %s\n", err.Error())
    }
	now := time.Now().In(loc)
	before := now.Add(hoursInThePast * time.Hour)
	times.startHour = strconv.Itoa(before.Hour())
	times.endHour = strconv.Itoa(now.Hour())
	ny, nm, nd := now.Date()
	by, bm, bd := before.Date()
	times.startDate = fmt.Sprintf("%04d-%02d-%02d", by, bm, bd)
	times.endDate = fmt.Sprintf("%04d-%02d-%02d", ny, nm, nd)
	return times
}

func getElectrictyPrices(times *Times) []float32 {
	var prices []float32

	infoLogger.Println("------- Function Call: GetImPriceE vnitrodenna cena-------")

	if times.startDate != times.endDate {
		htr := GetImPriceE(times.startDate, times.startDate, times.startHour, "24")
		if htr == nil {
			errorLogger.Println("HTTP Error, exiting.")
			return prices
		}
		prices1, err := extractPricesFromGetImPriceE(htr)
		if err != nil {
			infoLogger.Println("Error getting prices from previous day, continuing on second.")
		}
		htr = GetImPriceE(times.endDate, times.endDate, "0", times.endHour)
		if htr == nil {
			errorLogger.Println("HTTP Error, exiting.")
			return prices
		}
		prices2, err := extractPricesFromGetImPriceE(htr)
		if err != nil {
			errorLogger.Println("Error getting prices from this day, exiting.")
			return prices
		}
		prices = slices.Concat(prices1, prices2)
	} else {
		htr := GetImPriceE(times.startDate, times.endDate, times.startHour, times.endHour)
		if htr == nil {
			errorLogger.Println("HTTP Error, exiting.")
			return prices
		}
		var err error
		prices, err = extractPricesFromGetImPriceE(htr)
		if err != nil {
			errorLogger.Println("Error getting prices from today, exiting.")
			return prices
		}
	}
	return prices
}

func scaleCPUFrequency(prices []float32) {
	// A stupid basic comparator; will need redesign
	dec, inc := 0, 0
	for i := 0; i < len(prices)-1; i++ {
		if prices[i+1] <= prices[i] {
			dec += 1
		} else {
			inc += 1
		}
	}

	frequencies := getAvailableCPUFrequencies("/sys/devices/system/cpu/cpu0/cpufreq/scaling_available_frequencies")
	minF, maxF := 10000000, 0
	for _, frequency := range frequencies {
		if f, err := strconv.Atoi(frequency); err == nil {
			if f > maxF {
				maxF = f
			}
			if f < minF {
				minF = f
			}
		}
	}
	if dec < inc {
		infoLogger.Println("Prices are increasing over the last three hours")
		for i := 0; i < runtime.NumCPU()-1; i++ {
			err := writeFile(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_max_freq", i), fmt.Sprintf("%d", minF))
			if err != nil {
				infoLogger.Printf("Not scaling cpu%d to frequency %d\n", i, minF)
			} else {
				infoLogger.Printf("Scaling cpu%d to frequency %d\n", i, minF)
			}
		}
	} else {
		infoLogger.Println("Prices are decreasing over the last three hours.")
		for i := 0; i < runtime.NumCPU()-1; i++ {
			err := writeFile(fmt.Sprintf("/sys/devices/system/cpu/cpu%d/cpufreq/scaling_max_freq", i), fmt.Sprintf("%d", maxF))
			if err != nil {
				infoLogger.Printf("Not scaling cpu%d to frequency %d\n", i, maxF)
			} else {
				infoLogger.Printf("Scaling cpu%d to frequency %d\n", i, maxF)
			}
		}
	}
}

func readFile(path string) string {
	if _, err := os.Stat(path); e.Is(err, os.ErrNotExist) {
		errorLogger.Printf("Path %s does not exist\n", path)
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		errorLogger.Printf("Failed to open path %s\n", path)
		return ""
	}
	return string(content)
}

func writeFile(path, frequency string) error {
	err := os.WriteFile(path, []byte(frequency), 0644)
	if err != nil {
		errorLogger.Printf("Error writing frequency %s to path %s: %s\n", frequency, path, err.Error())
		return err
	}
	return nil
}

func getAvailableCPUFrequencies(path string) []string {
	fc := readFile(path)
	if fc == "" {
		return nil
	}
	return strings.Split(fc, " ")
}

func getEnvironmentVariables() {
	var err error
	hours := os.Getenv("HOURS")
	if len(hours) == 0 {
		hoursInThePast = -3
	} else {
		hoursInThePast, err = time.ParseDuration(hours)
		if err != nil {
			hoursInThePast = -3
			infoLogger.Printf("Error parsing hours %s to duration. Setting -3.\n", hours)
		}
	}
	wsdls := os.Getenv("WSDL")
	if len(wsdls) == 0 {
		wsdlService = "https://www.ote-cr.cz/services/PublicDataService"
	} else {
		wsdlService = wsdls
	}
}

func main() {
	getEnvironmentVariables()
	times := getTimeRange()
	prices := getElectrictyPrices(times)
	scaleCPUFrequency(prices)
}
