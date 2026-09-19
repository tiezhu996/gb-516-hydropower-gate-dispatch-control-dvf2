<script setup lang="ts">
import { computed, reactive, ref } from 'vue';
import { ElMessage } from 'element-plus';
import type { DomainRecord, DispatchPermit } from '../../types/domain';
import { PERMIT_DURATION_OPTIONS } from '../../types/status';
import { formatDate, permitStatusLabel } from '../../utils/format';
import { useAuth } from '../../hooks/useAuth';
import { useDispatchPermitStore } from '../../stores/dispatch-permit';
import ConfirmDialog from './ConfirmDialog.vue';
import PermitBadge from './PermitBadge.vue';

const props = defineProps<{ directives: DomainRecord[]; loading?: boolean }>();
const { can } = useAuth();
const permitStore = useDispatchPermitStore();

const applyTarget = ref<DomainRecord | null>(null);
const issueTarget = ref<DispatchPermit | null>(null);
const revokeTarget = ref<DispatchPermit | null>(null);
const applyForm = reactive({ durationMinutes: 30, reason: '' });
const issueReason = ref('');
const revokeReason = ref('');

const approvableDirectives = computed(() =>
  props.directives.filter((item) => item.status === 'approved'),
);

const permitByDirective = computed(() => permitStore.byDirective);

function canApply(directive: DomainRecord): boolean {
  if (!can('operator', 'admin') || directive.status !== 'approved') return false;
  const permit = permitByDirective.value[directive.id];
  return !permit || ['revoked', 'expired', 'invalidated'].includes(permit.status);
}

function canIssue(permit?: DispatchPermit): boolean {
  return can('reviewer', 'admin') && permit?.status === 'pending';
}

function canRevoke(permit?: DispatchPermit): boolean {
  return can('reviewer', 'admin') && permit?.status === 'active' && !permit.expired;
}

const applyTitle = computed(() => `申请限时调度许可 · ${applyTarget.value?.code || ''}`);
const applyGateLabel = computed(() => applyTarget.value ? `${applyTarget.value.relatedCode} · ${applyTarget.value.name}` : '');

function openApply(directive: DomainRecord): void {
  applyTarget.value = directive;
  applyForm.durationMinutes = 30;
  applyForm.reason = '现场作业需要限时闸门调度许可';
}

async function submitApply(): Promise<void> {
  if (!applyTarget.value || applyForm.reason.trim().length < 3) return;
  try {
    await permitStore.apply({
      directiveCode: applyTarget.value.code,
      gateCode: applyTarget.value.relatedCode,
      durationMinutes: applyForm.durationMinutes,
      reason: applyForm.reason.trim(),
    });
    ElMessage.success('调度许可申请已提交，等待复核员签发');
    applyTarget.value = null;
  } catch {
    // store surfaces the message on the page
  }
}

function openIssue(permit: DispatchPermit): void {
  issueTarget.value = permit;
  issueReason.value = '已复核：闸门无生效许可、指令已批准，准予限时调度';
}

async function submitIssue(): Promise<void> {
  if (!issueTarget.value || issueReason.value.trim().length < 3) return;
  try {
    await permitStore.issue(issueTarget.value.id, issueTarget.value.version, issueReason.value.trim());
    ElMessage.success('调度许可已签发');
    issueTarget.value = null;
  } catch {
    // surfaced by store
  }
}

function openRevoke(permit: DispatchPermit): void {
  revokeTarget.value = permit;
  revokeReason.value = '';
}

async function submitRevoke(): Promise<void> {
  if (!revokeTarget.value || revokeReason.value.trim().length < 3) return;
  try {
    await permitStore.revoke(revokeTarget.value.id, revokeReason.value.trim());
    ElMessage.success('调度许可已撤销');
    revokeTarget.value = null;
  } catch {
    // surfaced by store
  }
}
</script>

<template>
  <section class="permit-panel" aria-label="闸门调度许可">
    <header class="permit-panel__header">
      <div>
        <strong>闸门调度许可</strong>
        <span>操作员对已批准指令申请限时许可；复核员仅在闸门无生效许可时签发，执行前强制校验许可有效、未撤销且闸门一致。</span>
      </div>
    </header>

    <el-alert v-if="permitStore.error" :title="permitStore.error" type="error" show-icon :closable="false" />

    <el-table v-loading="loading || permitStore.loading" :data="permitStore.items" empty-text="暂无调度许可，可在已批准指令上申请" size="small">
      <el-table-column prop="code" label="许可编号" width="120" />
      <el-table-column label="指令 / 闸门" min-width="170">
        <template #default="{ row }">
          <strong>{{ row.directiveCode }}</strong>
          <small>{{ row.gateCode }}</small>
        </template>
      </el-table-column>
      <el-table-column label="状态" width="100">
        <template #default="{ row }"><PermitBadge :permit="row" /></template>
      </el-table-column>
      <el-table-column label="有效期" min-width="210">
        <template #default="{ row }">
          <span v-if="row.validFrom && row.validUntil" class="permit-window">
            {{ formatDate(row.validFrom) }}<br />至 {{ formatDate(row.validUntil) }}
            <em v-if="row.expired" class="permit-expired-text">（已过期）</em>
          </span>
          <span v-else class="muted">签发后生效</span>
        </template>
      </el-table-column>
      <el-table-column label="签发人" width="110">
        <template #default="{ row }">{{ row.issuedBy || '-' }}</template>
      </el-table-column>
      <el-table-column label="撤销原因 / 失效说明" min-width="200">
        <template #default="{ row }">
          <span v-if="row.revokeReason" class="permit-revoke">撤销：{{ row.revokeReason }}</span>
          <span v-else-if="row.invalidateCause" class="muted">{{ row.invalidateCause }}</span>
          <span v-else-if="row.expired" class="permit-expired-text">许可已超过有效期</span>
          <span v-else class="muted">-</span>
        </template>
      </el-table-column>
      <el-table-column label="操作" width="190" fixed="right">
        <template #default="{ row }">
          <div class="permit-actions">
            <el-button v-if="canIssue(row)" link type="primary" @click="openIssue(row)">签发</el-button>
            <el-button v-if="canRevoke(row)" link type="danger" @click="openRevoke(row)">撤销</el-button>
            <span v-if="!canIssue(row) && !canRevoke(row)" class="muted">{{ permitStatusLabel(row.status) }}</span>
          </div>
        </template>
      </el-table-column>
    </el-table>

    <footer v-if="approvableDirectives.length" class="permit-quick-apply">
      <span class="muted">已批准指令快捷申请：</span>
      <el-button
        v-for="directive in approvableDirectives"
        :key="directive.id"
        size="small"
        :disabled="!canApply(directive)"
        @click="openApply(directive)"
      >
        {{ directive.code }} · {{ directive.relatedCode }}
      </el-button>
    </footer>

    <ConfirmDialog :model-value="Boolean(applyTarget)" :title="applyTitle" confirm-label="提交申请" :confirm-disabled="applyForm.reason.trim().length < 3" :loading="permitStore.loading" @update:model-value="applyTarget = null" @confirm="submitApply">
      <el-form label-position="top">
        <el-form-item label="目标闸门">
          <el-input :model-value="applyGateLabel" disabled />
        </el-form-item>
        <el-form-item label="有效时长">
          <el-select v-model="applyForm.durationMinutes">
            <el-option v-for="option in PERMIT_DURATION_OPTIONS" :key="option.value" :label="option.label" :value="option.value" />
          </el-select>
        </el-form-item>
        <el-form-item label="申请原因">
          <el-input v-model="applyForm.reason" type="textarea" :rows="3" maxlength="500" show-word-limit />
        </el-form-item>
        <p class="muted">申请仅在指令已批准且闸门无生效许可时可被签发；同闸门的其他待审申请将在签发时失效。</p>
      </el-form>
    </ConfirmDialog>

    <ConfirmDialog :model-value="Boolean(issueTarget)" title="签发闸门调度许可" confirm-label="确认签发" :confirm-disabled="issueReason.trim().length < 3" :loading="permitStore.loading" @update:model-value="issueTarget = null" @confirm="submitIssue">
      <p>许可编号 <strong>{{ issueTarget?.code }}</strong>，闸门 <strong>{{ issueTarget?.gateCode }}</strong>。签发后同闸门其他待审申请立即失效，许可在有效时长内允许执行一次。</p>
      <el-input v-model="issueReason" type="textarea" :rows="3" maxlength="500" show-word-limit aria-label="签发意见" />
    </ConfirmDialog>

    <ConfirmDialog :model-value="Boolean(revokeTarget)" title="撤销闸门调度许可" confirm-label="确认撤销" :confirm-disabled="revokeReason.trim().length < 3" :loading="permitStore.loading" @update:model-value="revokeTarget = null" @confirm="submitRevoke">
      <p class="permit-revoke">撤销后该许可立即失效，执行将被阻止；已执行的指令不会倒退。</p>
      <el-input v-model="revokeReason" type="textarea" :rows="3" maxlength="500" show-word-limit placeholder="请填写撤销原因" aria-label="撤销原因" />
    </ConfirmDialog>
  </section>
</template>
