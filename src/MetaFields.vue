<script setup>
import {computed} from 'vue';import {fieldAccess} from './domain.js';import {latestForm} from './metadata.js';
const props=defineProps(['state','user','tenantId','formId','record','readonly']);
const fields=computed(()=>(props.record?.formVersion?props.state.forms.find(f=>f.id===props.formId&&f.version===props.record.formVersion):latestForm(props.state,props.formId))?.fields||[]);
const mode=k=>fieldAccess(props.state,props.user,props.tenantId,k,props.formId);
</script>
<template><div v-if="fields.length" class="mt-5"><p class="config-heading">Additional fields</p><template v-for="f in fields" :key="f.key"><v-text-field v-if="mode(f.key)!=='hidden'" :model-value="record.data?.[f.key]??''" @update:model-value="v=>{if(!record.data)record.data={};record.data[f.key]=v}" :label="f.label+(f.required?' *':'')" :readonly="readonly||mode(f.key)!=='edit'" :type="f.type==='date'?'date':f.type==='number'?'number':'text'" :rules="f.required?[v=>!!String(v??'').trim()||'Required']:[]"/></template></div></template>
