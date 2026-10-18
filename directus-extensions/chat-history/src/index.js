import InterfaceComponent from './interface.vue';

export default {
	id: 'chat-history',
	name: 'Переписка (чат)',
	icon: 'forum',
	description: 'Показує пов’язані повідомлення у вигляді чату',
	component: InterfaceComponent,
	types: ['alias'],
	localTypes: ['o2m'],
	group: 'relational',
	relational: true,
	options: null,
};
