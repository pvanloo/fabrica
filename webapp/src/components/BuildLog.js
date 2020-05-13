import React, {Component} from 'react';
import api from "./api";
import {formatError, T} from "./Utils";
import {Row, Code, Notification, Button} from '@canonical/react-components'
import DetailsCard from "./DetailsCard";

class BuildLog extends Component {
    constructor(props) {
        super(props)
        this.state = {
            build: {},
            error: '',
            scrollLog: false,
        }
    }

    componentDidMount() {
        this.getData()
    }

    poll = () => {
        // Polls every 0.5s
        setTimeout(this.getData.bind(this), 500);
    }

    getData() {
        api.buildGet(this.props.buildId).then(response => {
            this.setState({build: response.data.record, error:''}, this.scrollToBottom)
        })
        .catch(e => {
            this.setState({error: formatError(e.response.data), message: ''});
        })
        .finally( ()=> {
            this.poll()
        })
    }

    scrollToBottom() {
        if (this.state.scrollLog) {
            window.scrollTo(0, document.body.clientHeight)
        }
    }

    changeScroll() {
        if (!this.state.scrollLog) {
            window.scrollTo(0, 0)
        }
    }

    handleScrollClick = (e) => {
        this.setState({scrollLog: !this.state.scrollLog}, this.changeScroll)
    }

    renderLog() {
        if (!this.state.build.logs) {return T('getting-ready')+ '\r\n'}

        return this.state.build.logs.map(l => {
            return l.message + '\r\n'
        })
    }

    render() {
        return (
            <Row>
                <h3>{T('build-log')}</h3>
                <Row>
                    {this.state.error ?
                        <Notification type="negative" status="Error:">{this.state.error}</Notification>
                    :
                        ''
                    }

                    <DetailsCard fields={[
                        {label: T('name'), value: this.state.build.name},
                        {label: T('repo'), value: this.state.build.repo},
                        {label: T('created'), value: this.state.build.created},
                        {label: T('status'), value: T(this.state.build.status)},
                        ]} />

                    {this.state.scrollLog ?
                        ''
                        :
                        <Button className="col-2" appearance="neutral" onClick={this.handleScrollClick}>{T('scroll-on')}</Button>
                    }

                    <Code className="log"s>
                        {this.renderLog()}
                    </Code>

                    {this.state.scrollLog ?
                        <Button className="col-2" appearance="brand" onClick={this.handleScrollClick}>{T('scroll-off')}</Button>
                        :
                        ''
                    }
                </Row>
            </Row>
        );
    }
}

export default BuildLog;