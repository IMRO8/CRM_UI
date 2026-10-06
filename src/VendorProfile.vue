<script setup>
const props=defineProps(['profile','editable']);const emit=defineEmits(['edit']);
const labels={name:'Vendor name',trade:'Trade / specialization',contactName:'Contact person',phone:'Phone',email:'Email',gstin:'GSTIN',address:'Address',vendorType:'Vendor type',pan:'PAN',notes:'Notes'};
</script>
<template><section v-if="profile" class="vendor-brief vendor-above"><div class="approval-head"><h3>Vendor profile · {{profile.name||'Project vendor'}}</h3><v-btn v-if="editable" prepend-icon="mdi-pencil-outline" variant="text" size="small" color="primary" @click="emit('edit')">Edit vendor</v-btn></div><dl><template v-for="(label,key) in labels" :key="key"><template v-if="key in profile"><dt>{{label}}</dt><dd>{{profile[key]||'—'}}</dd></template></template><template v-for="(value,key) in profile.vendorData" :key="key"><dt>{{key}}</dt><dd>{{value||'—'}}</dd></template></dl></section></template>
