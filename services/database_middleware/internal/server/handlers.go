// Package server Code in this file is mechanical: each handler decodes nothing, calls one store method and
// returns its result. Errors are mapped to application/problem+json by errorHandler.
package server

import (
	"context"

	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
)

// MfaUseStep accepts a TOTP time step once.
func (h *Handler) MfaUseStep(ctx context.Context, request api.MfaUseStepRequestObject) (api.MfaUseStepResponseObject, error) {
	out, err := h.s.MfaUseStep(ctx, *request.Body)
	if err != nil {
		return nil, err
	}
	return api.MfaUseStep200JSONResponse(out), nil
}

// DatabaseMiddlewareHealth is the liveness probe.
func (h *Handler) DatabaseMiddlewareHealth(_ context.Context, _ api.DatabaseMiddlewareHealthRequestObject) (api.DatabaseMiddlewareHealthResponseObject, error) {
	return api.DatabaseMiddlewareHealth200Response{}, nil
}

func (h *Handler) AccountDelete(ctx context.Context, request api.AccountDeleteRequestObject) (api.AccountDeleteResponseObject, error) {
	b := request.Body
	out, err := h.s.AccountDelete(ctx, b.UserId)
	if err != nil {
		return nil, err
	}
	return api.AccountDelete200JSONResponse(out), nil
}

func (h *Handler) AuditList(ctx context.Context, request api.AuditListRequestObject) (api.AuditListResponseObject, error) {
	b := request.Body
	out, err := h.s.AuditList(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.AuditList200JSONResponse(out), nil
}

func (h *Handler) AuditWrite(ctx context.Context, request api.AuditWriteRequestObject) (api.AuditWriteResponseObject, error) {
	b := request.Body
	if err := h.s.AuditWrite(ctx, *b); err != nil {
		return nil, err
	}
	return api.AuditWrite204Response{}, nil
}

func (h *Handler) BookingAssignDoctor(ctx context.Context, request api.BookingAssignDoctorRequestObject) (api.BookingAssignDoctorResponseObject, error) {
	b := request.Body
	out, err := h.s.BookingAssignDoctor(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.BookingAssignDoctor200JSONResponse(out), nil
}

func (h *Handler) BookingCancel(ctx context.Context, request api.BookingCancelRequestObject) (api.BookingCancelResponseObject, error) {
	b := request.Body
	out, err := h.s.BookingCancel(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.BookingCancel200JSONResponse(out), nil
}

func (h *Handler) BookingCreate(ctx context.Context, request api.BookingCreateRequestObject) (api.BookingCreateResponseObject, error) {
	b := request.Body
	out, err := h.s.BookingCreate(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.BookingCreate201JSONResponse(out), nil
}

func (h *Handler) BookingGet(ctx context.Context, request api.BookingGetRequestObject) (api.BookingGetResponseObject, error) {
	b := request.Body
	out, err := h.s.BookingGet(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.BookingGet200JSONResponse(out), nil
}

func (h *Handler) BookingList(ctx context.Context, request api.BookingListRequestObject) (api.BookingListResponseObject, error) {
	b := request.Body
	out, err := h.s.BookingList(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.BookingList200JSONResponse(out), nil
}

func (h *Handler) BookingUpdate(ctx context.Context, request api.BookingUpdateRequestObject) (api.BookingUpdateResponseObject, error) {
	b := request.Body
	out, err := h.s.BookingUpdate(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.BookingUpdate200JSONResponse(out), nil
}

func (h *Handler) BookingsCompletePast(ctx context.Context, _ api.BookingsCompletePastRequestObject) (api.BookingsCompletePastResponseObject, error) {
	out, err := h.s.BookingsCompletePast(ctx)
	if err != nil {
		return nil, err
	}
	return api.BookingsCompletePast200JSONResponse(out), nil
}

func (h *Handler) CalendarGetWindows(ctx context.Context, request api.CalendarGetWindowsRequestObject) (api.CalendarGetWindowsResponseObject, error) {
	b := request.Body
	out, err := h.s.CalendarGetWindows(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.CalendarGetWindows200JSONResponse(out), nil
}

func (h *Handler) DoctorGetSchedule(ctx context.Context, request api.DoctorGetScheduleRequestObject) (api.DoctorGetScheduleResponseObject, error) {
	b := request.Body
	out, err := h.s.DoctorGetSchedule(ctx, b.DoctorId)
	if err != nil {
		return nil, err
	}
	return api.DoctorGetSchedule200JSONResponse(out), nil
}

func (h *Handler) DoctorList(ctx context.Context, request api.DoctorListRequestObject) (api.DoctorListResponseObject, error) {
	b := request.Body
	out, err := h.s.DoctorList(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.DoctorList200JSONResponse(out), nil
}

func (h *Handler) DoctorSetSchedule(ctx context.Context, request api.DoctorSetScheduleRequestObject) (api.DoctorSetScheduleResponseObject, error) {
	b := request.Body
	out, err := h.s.DoctorSetSchedule(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.DoctorSetSchedule200JSONResponse(out), nil
}

func (h *Handler) EmailTokenConsume(ctx context.Context, request api.EmailTokenConsumeRequestObject) (api.EmailTokenConsumeResponseObject, error) {
	b := request.Body
	out, err := h.s.EmailTokenConsume(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.EmailTokenConsume200JSONResponse(out), nil
}

func (h *Handler) EmailTokenCreate(ctx context.Context, request api.EmailTokenCreateRequestObject) (api.EmailTokenCreateResponseObject, error) {
	b := request.Body
	if err := h.s.EmailTokenCreate(ctx, *b); err != nil {
		return nil, err
	}
	return api.EmailTokenCreate204Response{}, nil
}

func (h *Handler) MfaDelete(ctx context.Context, request api.MfaDeleteRequestObject) (api.MfaDeleteResponseObject, error) {
	b := request.Body
	if err := h.s.MfaDelete(ctx, b.UserId); err != nil {
		return nil, err
	}
	return api.MfaDelete204Response{}, nil
}

func (h *Handler) MfaSecretConfirm(ctx context.Context, request api.MfaSecretConfirmRequestObject) (api.MfaSecretConfirmResponseObject, error) {
	b := request.Body
	if err := h.s.MfaSecretConfirm(ctx, b.UserId); err != nil {
		return nil, err
	}
	return api.MfaSecretConfirm204Response{}, nil
}

func (h *Handler) MfaSecretGet(ctx context.Context, request api.MfaSecretGetRequestObject) (api.MfaSecretGetResponseObject, error) {
	b := request.Body
	out, err := h.s.MfaSecretGet(ctx, b.UserId)
	if err != nil {
		return nil, err
	}
	return api.MfaSecretGet200JSONResponse(out), nil
}

func (h *Handler) MfaSecretUpsert(ctx context.Context, request api.MfaSecretUpsertRequestObject) (api.MfaSecretUpsertResponseObject, error) {
	b := request.Body
	if err := h.s.MfaSecretUpsert(ctx, *b); err != nil {
		return nil, err
	}
	return api.MfaSecretUpsert204Response{}, nil
}

func (h *Handler) PatientGet(ctx context.Context, request api.PatientGetRequestObject) (api.PatientGetResponseObject, error) {
	b := request.Body
	out, err := h.s.PatientGet(ctx, b.PatientId)
	if err != nil {
		return nil, err
	}
	return api.PatientGet200JSONResponse(out), nil
}

func (h *Handler) PatientList(ctx context.Context, request api.PatientListRequestObject) (api.PatientListResponseObject, error) {
	b := request.Body
	out, err := h.s.PatientList(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.PatientList200JSONResponse(out), nil
}

func (h *Handler) PatientSetMainDoctor(ctx context.Context, request api.PatientSetMainDoctorRequestObject) (api.PatientSetMainDoctorResponseObject, error) {
	b := request.Body
	out, err := h.s.PatientSetMainDoctor(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.PatientSetMainDoctor200JSONResponse(out), nil
}

func (h *Handler) PurgeExpired(ctx context.Context, request api.PurgeExpiredRequestObject) (api.PurgeExpiredResponseObject, error) {
	b := request.Body
	out, err := h.s.PurgeExpired(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.PurgeExpired200JSONResponse(out), nil
}

func (h *Handler) RecoveryCodeConsume(ctx context.Context, request api.RecoveryCodeConsumeRequestObject) (api.RecoveryCodeConsumeResponseObject, error) {
	b := request.Body
	out, err := h.s.RecoveryCodeConsume(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.RecoveryCodeConsume200JSONResponse(out), nil
}

func (h *Handler) RecoveryCodesReplace(ctx context.Context, request api.RecoveryCodesReplaceRequestObject) (api.RecoveryCodesReplaceResponseObject, error) {
	b := request.Body
	if err := h.s.RecoveryCodesReplace(ctx, *b); err != nil {
		return nil, err
	}
	return api.RecoveryCodesReplace204Response{}, nil
}

func (h *Handler) RetentionGet(ctx context.Context, _ api.RetentionGetRequestObject) (api.RetentionGetResponseObject, error) {
	out, err := h.s.RetentionGet(ctx)
	if err != nil {
		return nil, err
	}
	return api.RetentionGet200JSONResponse(out), nil
}

func (h *Handler) RetentionSet(ctx context.Context, request api.RetentionSetRequestObject) (api.RetentionSetResponseObject, error) {
	b := request.Body
	out, err := h.s.RetentionSet(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.RetentionSet200JSONResponse(out), nil
}

func (h *Handler) ServiceCreate(ctx context.Context, request api.ServiceCreateRequestObject) (api.ServiceCreateResponseObject, error) {
	b := request.Body
	out, err := h.s.ServiceCreate(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.ServiceCreate201JSONResponse(out), nil
}

func (h *Handler) ServiceDelete(ctx context.Context, request api.ServiceDeleteRequestObject) (api.ServiceDeleteResponseObject, error) {
	b := request.Body
	if err := h.s.ServiceDelete(ctx, b.Id); err != nil {
		return nil, err
	}
	return api.ServiceDelete204Response{}, nil
}

func (h *Handler) ServiceImport(ctx context.Context, request api.ServiceImportRequestObject) (api.ServiceImportResponseObject, error) {
	b := request.Body
	out, err := h.s.ServiceImport(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.ServiceImport200JSONResponse(out), nil
}

func (h *Handler) ServiceList(ctx context.Context, _ api.ServiceListRequestObject) (api.ServiceListResponseObject, error) {
	out, err := h.s.ServiceList(ctx)
	if err != nil {
		return nil, err
	}
	return api.ServiceList200JSONResponse(out), nil
}

func (h *Handler) ServiceUpdate(ctx context.Context, request api.ServiceUpdateRequestObject) (api.ServiceUpdateResponseObject, error) {
	b := request.Body
	out, err := h.s.ServiceUpdate(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.ServiceUpdate200JSONResponse(out), nil
}

func (h *Handler) SpecialtyCreate(ctx context.Context, request api.SpecialtyCreateRequestObject) (api.SpecialtyCreateResponseObject, error) {
	b := request.Body
	out, err := h.s.SpecialtyCreate(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.SpecialtyCreate201JSONResponse(out), nil
}

func (h *Handler) SpecialtyDelete(ctx context.Context, request api.SpecialtyDeleteRequestObject) (api.SpecialtyDeleteResponseObject, error) {
	b := request.Body
	if err := h.s.SpecialtyDelete(ctx, b.Id); err != nil {
		return nil, err
	}
	return api.SpecialtyDelete204Response{}, nil
}

func (h *Handler) SpecialtyList(ctx context.Context, _ api.SpecialtyListRequestObject) (api.SpecialtyListResponseObject, error) {
	out, err := h.s.SpecialtyList(ctx)
	if err != nil {
		return nil, err
	}
	return api.SpecialtyList200JSONResponse(out), nil
}

func (h *Handler) TokenCheck(ctx context.Context, request api.TokenCheckRequestObject) (api.TokenCheckResponseObject, error) {
	b := request.Body
	out, err := h.s.TokenCheck(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.TokenCheck200JSONResponse(out), nil
}

func (h *Handler) TokenRevoke(ctx context.Context, request api.TokenRevokeRequestObject) (api.TokenRevokeResponseObject, error) {
	b := request.Body
	if err := h.s.TokenRevoke(ctx, *b); err != nil {
		return nil, err
	}
	return api.TokenRevoke204Response{}, nil
}

func (h *Handler) UserChangeEmail(ctx context.Context, request api.UserChangeEmailRequestObject) (api.UserChangeEmailResponseObject, error) {
	b := request.Body
	if err := h.s.UserChangeEmail(ctx, *b); err != nil {
		return nil, err
	}
	return api.UserChangeEmail204Response{}, nil
}

func (h *Handler) UserCreate(ctx context.Context, request api.UserCreateRequestObject) (api.UserCreateResponseObject, error) {
	b := request.Body
	out, err := h.s.UserCreate(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.UserCreate201JSONResponse(out), nil
}

func (h *Handler) UserGetByEmail(ctx context.Context, request api.UserGetByEmailRequestObject) (api.UserGetByEmailResponseObject, error) {
	b := request.Body
	out, err := h.s.UserGetByEmail(ctx, string(b.Email))
	if err != nil {
		return nil, err
	}
	return api.UserGetByEmail200JSONResponse(out), nil
}

func (h *Handler) UserGetById(ctx context.Context, request api.UserGetByIdRequestObject) (api.UserGetByIdResponseObject, error) {
	b := request.Body
	out, err := h.s.UserGetByID(ctx, b.UserId)
	if err != nil {
		return nil, err
	}
	return api.UserGetById200JSONResponse(out), nil
}

func (h *Handler) UserList(ctx context.Context, request api.UserListRequestObject) (api.UserListResponseObject, error) {
	b := request.Body
	out, err := h.s.UserList(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.UserList200JSONResponse(out), nil
}

func (h *Handler) UserRecordLoginFailure(ctx context.Context, request api.UserRecordLoginFailureRequestObject) (api.UserRecordLoginFailureResponseObject, error) {
	b := request.Body
	out, err := h.s.UserRecordLoginFailure(ctx, b.UserId)
	if err != nil {
		return nil, err
	}
	return api.UserRecordLoginFailure200JSONResponse(out), nil
}

func (h *Handler) UserRecordLoginSuccess(ctx context.Context, request api.UserRecordLoginSuccessRequestObject) (api.UserRecordLoginSuccessResponseObject, error) {
	b := request.Body
	if err := h.s.UserRecordLoginSuccess(ctx, b.UserId); err != nil {
		return nil, err
	}
	return api.UserRecordLoginSuccess204Response{}, nil
}

func (h *Handler) UserRevokeSessions(ctx context.Context, request api.UserRevokeSessionsRequestObject) (api.UserRevokeSessionsResponseObject, error) {
	b := request.Body
	if err := h.s.UserRevokeSessions(ctx, b.UserId); err != nil {
		return nil, err
	}
	return api.UserRevokeSessions204Response{}, nil
}

func (h *Handler) UserSetEmailVerified(ctx context.Context, request api.UserSetEmailVerifiedRequestObject) (api.UserSetEmailVerifiedResponseObject, error) {
	b := request.Body
	if err := h.s.UserSetEmailVerified(ctx, b.UserId); err != nil {
		return nil, err
	}
	return api.UserSetEmailVerified204Response{}, nil
}

func (h *Handler) UserSetPassword(ctx context.Context, request api.UserSetPasswordRequestObject) (api.UserSetPasswordResponseObject, error) {
	b := request.Body
	if err := h.s.UserSetPassword(ctx, *b); err != nil {
		return nil, err
	}
	return api.UserSetPassword204Response{}, nil
}

func (h *Handler) UserSetRoles(ctx context.Context, request api.UserSetRolesRequestObject) (api.UserSetRolesResponseObject, error) {
	b := request.Body
	out, err := h.s.UserSetRoles(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.UserSetRoles200JSONResponse(out), nil
}

func (h *Handler) UserUnlock(ctx context.Context, request api.UserUnlockRequestObject) (api.UserUnlockResponseObject, error) {
	b := request.Body
	if err := h.s.UserUnlock(ctx, b.UserId); err != nil {
		return nil, err
	}
	return api.UserUnlock204Response{}, nil
}

func (h *Handler) UserUpdateProfile(ctx context.Context, request api.UserUpdateProfileRequestObject) (api.UserUpdateProfileResponseObject, error) {
	b := request.Body
	out, err := h.s.UserUpdateProfile(ctx, *b)
	if err != nil {
		return nil, err
	}
	return api.UserUpdateProfile200JSONResponse(out), nil
}
