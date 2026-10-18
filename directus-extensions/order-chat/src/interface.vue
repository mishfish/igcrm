<template>
	<div class="chat-history">
		<div v-if="!contactId" class="status">Спочатку обери клієнта</div>
		<div v-else-if="loading" class="status">Завантаження…</div>
		<div v-else-if="error" class="status error">Не вдалося завантажити переписку</div>
		<div v-else-if="!messages.length" class="status">Повідомлень ще немає</div>
		<div v-else class="messages">
			<div v-for="m in messages" :key="m.id" class="bubble-row" :class="m.direction">
				<div class="bubble">
					<div class="text">{{ m.text }}</div>
					<div class="time">{{ formatTime(m.sent_at) }}</div>
				</div>
			</div>
		</div>
	</div>
</template>

<script>
import { ref, inject, watch } from 'vue';
import { useApi } from '@directus/extensions-sdk';

export default {
	setup() {
		const api = useApi();
		// значення поточної форми (включно з ще не збереженими правками), контракт перевірено
		// на core-інтерфейсі "presentation-links": inject(`values`, ref({}))
		const values = inject('values', ref({}));
		const messages = ref([]);
		const loading = ref(false);
		const error = ref(false);
		const contactId = ref(null);

		async function load(id) {
			if (!id) {
				messages.value = [];
				return;
			}
			loading.value = true;
			error.value = false;
			try {
				const res = await api.get('/items/messages', {
					params: {
						filter: { contact_id: { _eq: id } },
						sort: 'sent_at',
						limit: 500,
						fields: 'id,direction,text,sent_at',
					},
				});
				messages.value = res.data.data || [];
			} catch (e) {
				error.value = true;
			} finally {
				loading.value = false;
			}
		}

		watch(
			() => values.value?.contact_id,
			(id) => {
				contactId.value = id ?? null;
				load(contactId.value);
			},
			{ immediate: true }
		);

		function formatTime(iso) {
			if (!iso) return '';
			const d = new Date(iso);
			return d.toLocaleString('uk-UA', {
				day: '2-digit',
				month: '2-digit',
				hour: '2-digit',
				minute: '2-digit',
			});
		}

		return { messages, loading, error, contactId, formatTime };
	},
};
</script>

<style scoped>
.chat-history {
	max-height: 440px;
	overflow-y: auto;
	border: var(--theme--border-width, 1px) solid var(--theme--border-color, #d3dae4);
	border-radius: var(--theme--border-radius, 6px);
	padding: 12px;
	background-color: var(--theme--background-subdued, #f7f8fb);
}

.status {
	color: var(--theme--foreground-subdued, #8196a3);
	text-align: center;
	padding: 24px;
	font-size: 14px;
}

.status.error {
	color: var(--theme--danger, #e35169);
}

.messages {
	display: flex;
	flex-direction: column;
	gap: 6px;
}

.bubble-row {
	display: flex;
}

.bubble-row.in {
	justify-content: flex-start;
}

.bubble-row.out {
	justify-content: flex-end;
}

.bubble {
	max-width: 72%;
	padding: 8px 12px;
	border-radius: 14px;
	font-size: 14px;
	line-height: 1.45;
	white-space: pre-wrap;
	word-break: break-word;
}

.bubble-row.in .bubble {
	background-color: var(--theme--background-normal, #ffffff);
	border: 1px solid var(--theme--border-color, #d3dae4);
	border-bottom-left-radius: 4px;
}

.bubble-row.out .bubble {
	background-color: var(--theme--primary, #6644ff);
	color: #ffffff;
	border-bottom-right-radius: 4px;
}

.time {
	font-size: 10px;
	opacity: 0.65;
	margin-top: 3px;
	text-align: right;
}
</style>
