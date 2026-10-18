import InterfaceComponent from './interface.vue';

export default {
	id: 'order-chat',
	name: 'Переписка клієнта (чат)',
	icon: 'forum',
	description: 'Показує переписку клієнта цього замовлення',
	component: InterfaceComponent,
	types: ['alias'],
	group: 'other',
	options: null,
};
