package server

import (
	"context"
	"sort"
	"time"

	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/api"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/db"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/mwclient"
	"github.com/juliandevelops-studium/my-doctors-date/services/calendar_service/internal/slots"
)

func toSlots(in []mwclient.Interval) []slots.Interval {
	out := make([]slots.Interval, len(in))
	for i, v := range in {
		out[i] = slots.Interval{Start: v.StartsAt, End: v.EndsAt}
	}
	return out
}

// checkRange validates a calendar query range: positive and at most 62 days (docs/api/CONVENTIONS.md).
func checkRange(from, to time.Time) *apiError {
	if !to.After(from) {
		return validation("to", "before_from")
	}
	if to.Sub(from) > MaxRange {
		return validation("to", "range_too_large")
	}
	return nil
}

// service loads one service of the Leistungsangebot.
func (s *Server) service(ctx context.Context, id db.Uuid) (*mwclient.Service, error) {
	l, err := s.DB.ServiceList(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	for i := range l.Items {
		if l.Items[i].Id == id {
			return &l.Items[i], nil
		}
	}
	return nil, notFound()
}

// GetCalendarBlocks returns free and busy blocks without any detail (UF-05, UF-08).
func (s *Server) GetCalendarBlocks(ctx context.Context, req api.GetCalendarBlocksRequestObject) (api.GetCalendarBlocksResponseObject, error) {
	p := req.Params
	if e := checkRange(p.From, p.To); e != nil {
		return nil, e
	}
	res, err := s.DB.CalendarGetWindows(ctx, mwclient.WindowsParams{From: p.From, To: p.To, DoctorId: p.DoctorId})
	if err != nil {
		return nil, mapErr(err)
	}
	window := slots.Interval{Start: p.From, End: p.To}
	out := api.BlockList{Items: []api.Block{}}
	for _, d := range res.Doctors {
		for _, b := range slots.Blocks(toSlots(d.Working), toSlots(d.Busy)) {
			if !b.Overlaps(window) {
				continue
			}
			if b.Start.Before(window.Start) {
				b.Start = window.Start
			}
			if b.End.After(window.End) {
				b.End = window.End
			}
			out.Items = append(out.Items, api.Block{DoctorId: d.DoctorId, StartsAt: b.Start, EndsAt: b.End, State: api.BlockState(b.State)})
		}
	}
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].StartsAt.Before(out.Items[j].StartsAt) })
	return api.GetCalendarBlocks200JSONResponse(out), nil
}

// patientFor returns the patient a booking or an availability query is for: the caller for a patient, the given
// patient for staff (a staff member who is also a patient may omit it). nil means "no patient".
func (s *Server) patientFor(id Identity, given *db.Uuid) *db.Uuid {
	if id.staff() && given != nil {
		return given
	}
	if id.has("patient") {
		u := id.UserID
		return &u
	}
	return nil
}

// GetAvailability lists the bookable start times of a service (UF-05, UF-06).
//
//   - A patient with a main doctor sees only that doctor's times, even if other doctors offer the service.
//   - A patient without one sees the union over all doctors with the matching specialty, without doctor_id
//     (the doctor is assigned when booking).
//   - "Sonstiges" has no specialty: the union over all doctors, or the main doctor.
//   - Staff always see the doctor of each time.
func (s *Server) GetAvailability(ctx context.Context, req api.GetAvailabilityRequestObject) (api.GetAvailabilityResponseObject, error) {
	id := identityFrom(ctx)
	p := req.Params
	if e := checkRange(p.From, p.To); e != nil {
		return nil, e
	}
	svc, err := s.service(ctx, p.ServiceId)
	if err != nil {
		return nil, err
	}
	var main *db.Uuid
	if pid := s.patientFor(id, p.PatientId); pid != nil {
		pat, err := s.DB.PatientGet(ctx, *pid)
		if err != nil {
			return nil, mapErr(err)
		}
		main = pat.MainDoctorId
	}
	q := mwclient.WindowsParams{From: p.From, To: p.To, DoctorId: main}
	if !svc.IsOther {
		q.SpecialtyId = svc.SpecialtyId
	}
	res, err := s.DB.CalendarGetWindows(ctx, q)
	if err != nil {
		return nil, mapErr(err)
	}
	duration := time.Duration(svc.DurationMinutes) * time.Minute
	showDoctor := id.staff() || main != nil
	out := api.SlotList{Items: []api.Slot{}}
	seen := map[time.Time]bool{}
	var all []api.Slot
	for _, d := range res.Doctors {
		for _, sl := range slots.Bookable(toSlots(d.Working), toSlots(d.Busy), duration, SlotStep, s.Now()) {
			if sl.Start.Before(p.From) || sl.End.After(p.To) {
				continue
			}
			item := api.Slot{StartsAt: sl.Start, EndsAt: sl.End}
			if showDoctor {
				did := d.DoctorId
				item.DoctorId = &did
			} else if seen[sl.Start] {
				continue
			}
			seen[sl.Start] = true
			all = append(all, item)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].StartsAt.Before(all[j].StartsAt) })
	out.Items = append(out.Items, all...)
	return api.GetAvailability200JSONResponse(out), nil
}
