<script setup lang="ts">
import { computed } from 'vue';
import { CircleCheck, CircleClose, Clock, Select, Warning } from '@element-plus/icons-vue';
import type { DispatchPermit } from '../../types/domain';
import { permitStatusLabel } from '../../utils/format';

const props = defineProps<{ permit?: DispatchPermit }>();

const tone = computed(() => {
  const status = props.permit?.status;
  if (props.permit?.expired || status === 'expired') return 'expired';
  if (status === 'active' && props.permit?.usedAt) return 'used';
  if (status === 'active' && props.permit?.effective) return 'active';
  if (status === 'revoked' || status === 'invalidated') return 'closed';
  return 'pending';
});

const label = computed(() => {
  if (props.permit?.expired && props.permit.status === 'active') return '已过期';
  if (props.permit?.status === 'active' && props.permit.usedAt) return '已使用';
  return permitStatusLabel(props.permit?.status || '');
});

const icon = computed(() => ({ active: CircleCheck, used: Select, pending: Clock, expired: Warning, closed: CircleClose }[tone.value] || Clock));
</script>

<template>
  <span v-if="permit" :class="['permit-badge', `permit-badge--${tone}`]">
    <el-icon><component :is="icon" /></el-icon>
    {{ label }}
  </span>
  <span v-else class="permit-badge permit-badge--empty">无许可</span>
</template>
