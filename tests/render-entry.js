import PaymentSchedulePanel from '../src/PaymentSchedulePanel.vue';
import {createSSRApp} from 'vue';
import {renderToString} from 'vue/server-renderer';
import {createVuetify} from 'vuetify';
import * as components from 'vuetify/components';
import * as directives from 'vuetify/directives';
import App from '../src/App.vue';
import {themeOptions} from '../src/theme.js';
import VendorProfile from '../src/VendorProfile.vue';
import WorkOrderItems from '../src/WorkOrderItems.vue';
import ProjectsPanel from '../src/ProjectsPanel.vue';
import ProcurementPanel from '../src/ProcurementPanel.vue';
import FinancePanel from '../src/FinancePanel.vue';
import CostPanel from '../src/CostPanel.vue';
import OnboardingPanel from '../src/OnboardingPanel.vue';
import VendorSuitePanel from '../src/VendorSuitePanel.vue';
import DashboardPanel from '../src/DashboardPanel.vue';
import {seed} from '../src/domain.js';
const app=createSSRApp(App);app.use(createVuetify({components,directives,theme:themeOptions}));
const html=await renderToString(app);
for(const text of ['Work Orders','WO-SKY-001','CRM dashboard','VRISE','/vrise-logo.png','₹14,00,000.00']){if(!html.includes(text))throw Error('Missing rendered sample content: '+text);}
if(html.includes('<v-app'))throw Error('Vuetify component registration failed');
console.log('PASS: Vue and Vuetify render the populated financial dashboard.');

const s=seed();
async function panel(component,props){const app=createSSRApp(component,props);app.use(createVuetify({components,directives,theme:themeOptions}));return renderToString(app);}
const projects=await panel(ProjectsPanel,{state:s,user:s.users[0]});for(const text of ['Add project','Edit','Delete','Skyline Residences'])if(!projects.includes(text))throw Error('Projects missing '+text);
const regular=await panel(ProjectsPanel,{state:s,user:s.users[1]});if(regular.includes('Add project'))throw Error('Project controls visible to regular user');
const purchase=await panel(ProcurementPanel,{state:s,user:s.users[0],tenantId:'t1',page:'purchases'});for(const text of ['PO-SKY-001','ORDERED MATERIAL','SUPPLIER','APPROVED BY','APPROVED DATE','VALUE','New purchase order','₹0.00'])if(!purchase.includes(text))throw Error('Purchase view missing '+text);
const restricted=await panel(ProcurementPanel,{state:s,user:s.users[3],tenantId:'t1',page:'purchases'});if(restricted.includes('New purchase order')||restricted.includes('₹1,65,000.00'))throw Error('Restricted purchase controls or cost visible');
s.stockLots=[{id:'render-lot',material:'TMT steel',unit:'tonne',originTenantId:'t1'}];s.stockMovements=[{id:'render-receipt',lotId:'render-lot',tenantId:'t1',quantityMilli:1250,kind:'receipt',actorId:'u1',at:'2026-10-05T08:00:00Z'}];
const stock=await panel(ProcurementPanel,{state:s,user:s.users[0],tenantId:'t1',page:'inventory'});for(const text of ['TMT steel','1.25 tonne','Transfer','Issue for use','Stock movement history'])if(!stock.includes(text))throw Error('Inventory view missing '+text);
console.log('PASS: project controls, purchase/rental lines and inventory views render with role visibility.');

const profile=await panel(VendorProfile,{profile:s.vendors[0],editable:true});for(const text of ['Vendor profile','Srinivas Reddy','vendor1@example.com'])if(!profile.includes(text))throw Error('Vendor profile missing '+text);
const itemView=await panel(WorkOrderItems,{order:{purchaseOrderNumber:'PO-SKY-001',purchaseOrderDate:'2026-10-05',items:[{description:'Cable installation',unit:'metre',quantityMilli:1250,rate:101,amount:126,startDate:'2026-10-01',endDate:'2026-10-31'}]},modes:{description:'view',unit:'view',quantity:'view',period:'view',amount:'view',purchase_reference:'view'}});for(const text of ['DESCRIPTION OF ITEM','UNIT','PERIOD','QUANTITY','RATE','AMOUNT','Cable installation','PO-SKY-001','1.25'])if(!itemView.includes(text))throw Error('Work-order fields missing '+text);
console.log('PASS: vendor profiles, PO approval columns and work-order item fields render.');

const expenses=await panel(FinancePanel,{state:s,user:s.users[0],tenantId:'t1',page:'expenses'});for(const text of ['Expense register','Consultant charges','Salaries','Admin costs','Add expense'])if(!expenses.includes(text))throw Error('Expense view missing '+text);
const payments=await panel(FinancePanel,{state:s,user:s.users[0],tenantId:'t1',page:'payments'});for(const text of ['PAID AMOUNT','TDS','PAID DATE','BALANCE AMOUNT','WORK ORDER NO.','WORK ORDER DATE','INVOICE NO.','INVOICE DATE','INVOICE STATUS','REMARKS','SSC-2026-041','Record payment'])if(!payments.includes(text))throw Error('Payments view missing '+text);
const costs=await panel(CostPanel,{state:s,user:s.users[0],tenantId:'t1'});if(!costs.includes('Total project cost')||!costs.includes('Consultant charges'))throw Error('Cost overview missing');
const noCosts=await panel(CostPanel,{state:s,user:s.users[2],tenantId:'t1'});if(noCosts.includes('₹'))throw Error('Cost overview leaked to Finance');
const onboarding=await panel(OnboardingPanel,{state:s,user:s.users[0],tenantId:'t1'});for(const text of ['Onboard vendor','Aster Structural Consultants','Submitted','Awaiting activation'])if(!onboarding.includes(text))throw Error('Onboarding missing '+text);
console.log('PASS: expense, payment, cost and onboarding screens render with required columns and access restrictions.');

const quotes=await panel(VendorSuitePanel,{state:s,user:s.users[0],tenantId:'t1',page:'quotations'});for(const text of ['Quotation comparison','QT-SKY-001','CREDIT DAYS','Raise work order','New work package'])if(!quotes.includes(text))throw Error('Quotation module missing '+text);
const feedback=await panel(VendorSuitePanel,{state:s,user:s.users[0],tenantId:'t1',page:'feedback'});for(const text of ['SAFETY','COMMUNICATION','Sri Sai Constructions','Add feedback'])if(!feedback.includes(text))throw Error('Feedback module missing '+text);
const reports=await panel(VendorSuitePanel,{state:s,user:s.users[0],tenantId:'t1',page:'vendorreports'});for(const text of ['Export CSV','Purchase purpose trail','PO-SKY-001','TMT steel'])if(!reports.includes(text))throw Error('Reporting module missing '+text);
const dashboard=await panel(DashboardPanel,{state:s,user:s.users[0],tenantId:'t1'});for(const text of ['Work progress','Cash inflows vs target','Budget &amp; targets','Booked sales value','Work orders → approved commitments','Measured'])if(!dashboard.includes(text))throw Error('CRM missing '+text);
const engineerDashboard=await panel(DashboardPanel,{state:s,user:s.users[3],tenantId:'t1'});if(engineerDashboard.includes('Cash inflows vs target')||engineerDashboard.includes('Booked sales value')||engineerDashboard.includes('Budget committed'))throw Error('CRM exposed restricted money');
console.log('PASS: quotation comparison, feedback, reporting and CRM charts render with access controls.');

for(const [page,words]of [['corporate',['Corporate account registry','Salary &amp; admin payments','Record corporate payment']],['bookings',['Booked units &amp; applicants','Complete booking','View profile']],['schedules',['Unit payment schedule','NET RECEIVABLE','Agreement','Awaiting completion','Configure schedule']]]){const html=await panel(PaymentSchedulePanel,{state:s,user:s.users[0],tenantId:'t1',page});for(const word of words)if(!html.includes(word))throw Error('New payment screens missing '+word);}
console.log('PASS: corporate accounts, booking profiles and reference instalment schedules render.');
