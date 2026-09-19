
import { request } from './client';
import type { GateDispatchPermit } from '../types/domain';

export async function listGateDispatchPermits(directiveCode?: string, gateCode?: string, status?: string) {
  const params = new URLSearchParams({ page: '1', pageSize: '100' });
  if (directiveCode) params.set('directiveCode', directiveCode);
  if (gateCode) params.set('gateCode', gateCode);
  if (status) params.set('status', status);
  return request<GateDispatchPermit[]>(`/permits?${params.toString()}`);
}

export async function applyGateDispatchPermit(input: {
  code: string; directiveId: number; gateCode: string; validMinutes: number; requestReason: string;
}) {
  return request<GateDispatchPermit>('/permits', { method: 'POST', body: JSON.stringify(input) });
}

export async function issueGateDispatchPermit(id: number, expectedVersion: number, issueReason: string) {
  return request<GateDispatchPermit>(`/permits/${id}/issue`, {
    method: 'POST', body: JSON.stringify({ expectedVersion, issueReason }),
  });
}

export async function revokeGateDispatchPermit(id: number, expectedVersion: number, revokeReason: string) {
  return request<GateDispatchPermit>(`/permits/${id}/revoke`, {
    method: 'POST', body: JSON.stringify({ expectedVersion, revokeReason }),
  });
}
