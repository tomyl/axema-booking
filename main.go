package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/adrg/xdg"
	ics "github.com/arran4/golang-ical"
)

const appName = "axema-booking"

type nonceResponse struct {
	Nonce1 string `json:"nonce1"`
	Nonce2 string `json:"nonce2"`
}

type loginRequest struct {
	Nonce1 string `json:"nonce1"`
	Nonce2 string `json:"nonce2"`
	Pass   string `json:"pass"`
	User   string `json:"user"`
}

type schedulePeriod struct {
	ID        int `json:"Id"`
	StartTime int `json:"StartTime"`
	StopTime  int `json:"StopTime"`
}

type weekDay struct {
	Date string `json:"Date"`
}

type week struct {
	WeekNumber int       `json:"WeekNumber"`
	WeekDays   []weekDay `json:"WeekDays"`
}

type laundryUnit struct {
	ID                 int                `json:"Id"`
	Name               string             `json:"Name"`
	WeekList           []week             `json:"WeekList"`
	SchedulePeriodList [][]schedulePeriod `json:"SchedulePeriodList"`
}

func (lu laundryUnit) GetDate(weekNumber, day int) string {
	for _, w := range lu.WeekList {
		if w.WeekNumber == weekNumber {
			if day < len(w.WeekDays) {
				return w.WeekDays[day].Date
			}
		}
	}
	return ""
}

func (lu laundryUnit) GetSchedulePeriodByID(id int) (int, schedulePeriod) {
	for day, item := range lu.SchedulePeriodList {
		for _, slot := range item {
			if slot.ID == id {
				return day, slot
			}
		}
	}
	return -1, schedulePeriod{}
}

type bookingViewRequest struct {
	ReservationObjectID int `json:"reservation_object_id"`
}

type bookingViewResponse struct {
	TimeStamp    string        `json:"TimeStamp"`
	LaundryUnits []laundryUnit `json:"LaundryUnits"`
}

func (r bookingViewResponse) GetSchedulePeriodByID(id int) (int, schedulePeriod, laundryUnit) {
	for _, lu := range r.LaundryUnits {
		day, period := lu.GetSchedulePeriodByID(id)
		if day >= 0 {
			return day, period, lu
		}
	}
	return -1, schedulePeriod{}, laundryUnit{}
}

type booking struct {
	ID               int `json:"Id"`
	ObjectID         int `json:"ObjectId"`
	WeekNumber       int `json:"WeekNumber"`
	SchedulePeriodID int `json:"SchedulePeriodId"`
}

type ownedReservationsResponse struct {
	Bookings []booking `json:"Bookings"`
}

type object struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type client struct {
	client   *http.Client
	jar      *cookiejar.Jar
	endpoint string
	user     string
	pass     string
	objects  map[int]bookingViewResponse
}

func newClient(endpoint, user, pass string) (*client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &client{
		client:   &http.Client{Jar: jar},
		jar:      jar,
		endpoint: endpoint,
		user:     user,
		pass:     pass,
	}, nil
}

func (c *client) nonce() (nonceResponse, error) {
	body, err := c.post("/fcgi_reservation/get_nonce", nil)
	if err != nil {
		return nonceResponse{}, err
	}

	var resp nonceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nonceResponse{}, err
	}

	return resp, nil
}

func (c *client) login() error {
	nonce, err := c.nonce()
	if err != nil {
		return err
	}

	payload := loginRequest{
		Nonce1: nonce.Nonce1,
		Nonce2: nonce.Nonce2,
		User:   getMD5Hash(nonce.Nonce1 + c.user),
		Pass:   getMD5Hash(nonce.Nonce2 + c.user + c.pass),
	}

	buf, err := json.Marshal(&payload)
	if err != nil {
		return err
	}

	body, err := c.post("/fcgi_reservation/login", bytes.NewReader(buf))
	if err != nil {
		return err
	}

	if strings.Contains(string(body), "wrong_login") {
		return fmt.Errorf("login: %s", string(body))
	}

	return nil
}

func (c *client) GetCachedBookingView() ([]object, error) {
	name := "request_booking_view.json"
	body, err := c.cache(name, func() ([]byte, error) {
		return c.requestBookingView()
	})
	if err != nil {
		return nil, err
	}

	var resp []object
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}

	return resp, nil
}

func (c *client) requestBookingView() ([]byte, error) {
	return c.post("/fcgi_reservation/request_booking_view", nil)
}

func (c *client) GetCachedBookingViewByObjectID(id int) (bookingViewResponse, error) {
	name := fmt.Sprintf("request_booking_view_%d.json", id)
	body, err := c.cache(name, func() ([]byte, error) {
		return c.requestBookingViewByObjectID(id)
	})
	if err != nil {
		return bookingViewResponse{}, err
	}

	var resp bookingViewResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return bookingViewResponse{}, err
	}

	return resp, nil
}

func (c *client) requestBookingViewByObjectID(id int) ([]byte, error) {
	payload := bookingViewRequest{
		ReservationObjectID: id,
	}

	buf, err := json.Marshal(&payload)
	if err != nil {
		return nil, err
	}

	return c.post("/fcgi_reservation/request_booking_view", bytes.NewReader(buf))
}

func (c *client) GetCachedOwnedReservations() (ownedReservationsResponse, error) {
	name := "request_owned_reservations.json"
	body, err := c.cache(name, func() ([]byte, error) {
		return c.requestOwnedReservations()
	})
	if err != nil {
		return ownedReservationsResponse{}, err
	}

	var resp ownedReservationsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ownedReservationsResponse{}, err
	}

	return resp, nil
}

func (c *client) GetOwnedReservations() (ownedReservationsResponse, error) {
	body, err := c.requestOwnedReservations()
	if err != nil {
		return ownedReservationsResponse{}, err
	}

	var resp ownedReservationsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return ownedReservationsResponse{}, err
	}

	return resp, nil
}

func (c *client) requestOwnedReservations() ([]byte, error) {
	return c.post("/fcgi_reservation/request_owned_reservations", nil)
}

func (c *client) cache(name string, fn func() ([]byte, error)) ([]byte, error) {
	cacheName, err := xdg.CacheFile(path.Join(appName, "cache", name))
	if err != nil {
		return nil, err
	}

	buf, err := os.ReadFile(cacheName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("caching %s", cacheName)
			buf, err = fn()
			if err != nil {
				return nil, err
			}
			file, err := os.Create(cacheName)
			if err != nil {
				return nil, err
			}
			defer file.Close()
			if _, err := file.Write(buf); err != nil {
				return nil, err
			}
			if err := file.Sync(); err != nil {
				return nil, err
			}
			return buf, nil
		}
		return nil, err
	}

	return buf, nil
}

func (c *client) post(name string, r io.Reader) ([]byte, error) {
	req, err := http.NewRequest("POST", c.endpoint+name, r)
	if err != nil {
		return nil, err
	}

	if r != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", name, string(body))
	}

	return body, nil
}

func getMD5Hash(s string) string {
	hash := md5.Sum([]byte(s))
	return hex.EncodeToString(hash[:])
}

func toTime(date string, mins int) (time.Time, error) {
	fields := strings.Split(date, "-")
	if len(fields) != 3 {
		return time.Time{}, fmt.Errorf("bad date %q", date)
	}
	year, yerr := strconv.Atoi(fields[0])
	month, merr := strconv.Atoi(fields[1])
	day, derr := strconv.Atoi(fields[2])
	err := errors.Join(yerr, merr, derr)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(year, time.Month(month), day, mins/60, mins-60*(mins/60), 0, 0, loc), nil
}

func run() error {
	endpoint := os.Getenv("AXEMA_ENDPOINT")
	if endpoint == "" {
		return errors.New("AXEMA_ENDPOINT not set")
	}

	user := os.Getenv("AXEMA_USER")
	if user == "" {
		return errors.New("AXEMA_USER not set")
	}

	pass := os.Getenv("AXEMA_PASS")
	if pass == "" {
		return errors.New("AXEMA_PASS not set")
	}

	c, err := newClient(endpoint, user, pass)
	if err != nil {
		return err
	}

	if err := c.login(); err != nil {
		return err
	}

	objects, err := c.GetCachedBookingView()
	if err != nil {
		return err
	}

	c.objects = make(map[int]bookingViewResponse, len(objects))
	for _, o := range objects {
		resp, err := c.GetCachedBookingViewByObjectID(o.ID)
		if err != nil {
			return err
		}
		c.objects[o.ID] = resp
	}

	reservations, err := c.GetOwnedReservations()
	if err != nil {
		return err
	}

	cal := ics.NewCalendar()

	for _, r := range reservations.Bookings {
		o, ok := c.objects[r.ObjectID]
		if !ok {
			return fmt.Errorf("bad object %d", r.ObjectID)
		}
		day, period, lu := o.GetSchedulePeriodByID(r.SchedulePeriodID)
		date := lu.GetDate(r.WeekNumber, day)
		if date == "" {
			return fmt.Errorf("failed to get date for reservation %d", r.ID)
		}
		start, err := toTime(date, period.StartTime)
		if err != nil {
			return err
		}
		stop, err := toTime(date, period.StopTime)
		if err != nil {
			return err
		}
		//fmt.Fprintf(os.Stdout, "w%d %s-%s %s\n", r.WeekNumber, start.Format("2006-01-02 15:04"), stop.Format("15:04"), lu.Name)
		event := cal.AddEvent(fmt.Sprintf("%s/%d/%d/%d", c.endpoint, r.ID, r.WeekNumber, r.SchedulePeriodID))
		event.SetStartAt(start)
		event.SetEndAt(stop)
		event.SetSummary(lu.Name)
	}

	os.Stdout.Write([]byte(cal.Serialize()))

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
