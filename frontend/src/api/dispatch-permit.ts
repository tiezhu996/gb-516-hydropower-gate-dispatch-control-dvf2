import { request } from './client';
import type { DispatchPermit, PageMeta } from '../types/domain';

export interface PermitQuery {
  page?: number;
  pageSize?: number;
  search?: string;
  status?: string;
  directiveCode?: string;
  gateCode?: string;
}

function queryString(query: PermitQuery): string {
  const params = new URLSearchParams();
  params.set('page', String(query.page ?? 1));
  params.set('pageSize', String(query.pageSize ?? 50));
  if (query.search) params.set('search', query.search);
  if (query.status) params.set('status', query.status);
  if (query.directiveCode) params.set('directiveCode', query.directiveCode);
  if (query.gateCode) params.set('gateCode', query.gateCode);
  return params.toString();
}

export async function listDispatchPermits(query: PermitQuery = {}) {
  return request<DispatchPermit[]>(`/permits?${queryString(query)}`);
}

export async function applyDispatchPermit(input: {
  directiveCode: string;
  gateCode?: string;
  durationMinutes: number;
  reason: string;
  code?: string;
}) {
  return request<DispatchPermit>('/permits', { method: 'POST', body: JSON.stringify(input) });
}

export async function issueDispatchPermit(id: number, expectedVersion: number, reason: string) {
  return request<DispatchPermit>(`/permits/${id}/issue`, {
    method: 'POST',
    body: JSON.stringify({ expectedVersion, reason }),
  });
}

export async function revokeDispatchPermit(id: number, reason: string) {
  return request<DispatchPermit>(`/permits/${id}/revoke`, {
    method: 'POST',
    body: JSON.stringify({ reason }),
  });
}

export type { DispatchPermit, PageMeta };
